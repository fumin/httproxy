package gvisor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

// Sandbox is a running gVisor sandbox in which commands can be executed.
// Commands executed by Exec share the same filesystem, processes and network.
type Sandbox struct {
	id    string
	state string
	// runscPid is the host PID of the `runsc run` process.
	// Its user and PID namespaces are the ones runsc expects to be run in.
	runscPid int

	cancel context.CancelFunc
	done   chan struct{}
	runErr error
}

// NewSandbox starts a sandbox and returns once commands can be executed in it.
// The sandbox is stopped when ctx is done, or when Close is called.
func NewSandbox(ctx context.Context, cfg Config) (*Sandbox, error) {
	// Start runsc in the background.
	sandboxCtx, sandboxCancel := context.WithCancel(ctx)
	s := &Sandbox{cancel: sandboxCancel, done: make(chan struct{})}
	started := make(chan struct{})
	go func() {
		defer func() {
			sandboxCancel()
			close(s.done)
		}()
		s.runErr = run(sandboxCtx, cfg, func(id, state string, runscPid int) {
			s.id, s.state, s.runscPid = id, state, runscPid
			close(started)
		})
	}()

	// Wait till runsc's poststart hook confirmed the container has started.
	poststartTimeoutCtx, pstoCancel := context.WithTimeout(sandboxCtx, 30*time.Second)
	defer pstoCancel()
	select {
	case <-started:
	case <-s.done:
		return nil, errors.Errorf("sandbox exited before starting: %+v", s.runErr)
	case <-poststartTimeoutCtx.Done():
		s.cancel()
		<-s.done
		if s.runErr != nil {
			return nil, errors.Errorf("sandbox exited before starting: %+v", s.runErr)
		}
		return nil, errors.Wrap(poststartTimeoutCtx.Err(), "sandbox not started")
	}
	return s, nil
}

// Close stops the sandbox.
func (s *Sandbox) Close() error {
	s.cancel()
	<-s.done
	if s.runErr != nil {
		return errors.Wrap(s.runErr, "")
	}
	return nil
}

// Done returns a channel that is closed when the sandbox exits.
func (s *Sandbox) Done() <-chan struct{} {
	return s.done
}

// Exec runs a command in the sandbox.
func (s *Sandbox) Exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, attr *syscall.SysProcAttr) error {
	// Create cmdCtx, the context runsc uses.
	// cmdCtx waits on ctx, and upon ctx.Done cleans up gVisor processes.
	runscExitedCtx, notifyRunscExited := context.WithCancelCause(context.Background())
	defer notifyRunscExited(nil)
	pidReader, err := newInternalPidReader(filepath.Dir(s.state))
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer pidReader.Close()
	cmdCtx, cleanupDone := newCmdCtx(ctx, runscExitedCtx, s.killInternalProc, pidReader.PidChan)

	// Create runsc command.
	runscArgs := s.subCommand(append([]string{"exec", "--internal-pid-file", pidReader.Path(), s.id}, args...)...)
	if attr != nil && attr.Setctty {
		// When the caller sets the controlling terminal, it likely
		// means the internal gVisor process is a terminal.
		// In this case, signals are sent to our helper processes as
		// well as runsc. We protect our helper processes from these
		// incoming signals, while runsc can forward them to gVisor.
		runscArgs = append([]string{envBin, "--ignore-signal=INT,QUIT,TSTP"}, runscArgs...)
	}
	cmd := exec.CommandContext(cmdCtx, runscArgs[0], runscArgs[1:]...)
	// Handle stdin ourselves, so we can return immediately when runsc exits
	// without waiting for stdin to close.
	var sinP io.WriteCloser
	if f, ok := stdin.(*os.File); ok {
		cmd.Stdin = f
	} else if stdin != nil {
		if sinP, err = cmd.StdinPipe(); err != nil {
			return errors.Wrap(err, "")
		}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = attr
	cmd.WaitDelay = time.Second

	// Start runsc and wait.
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "")
	}
	go func() {
		if sinP != nil {
			io.Copy(sinP, stdin)
			sinP.Close()
		}
	}()
	cmdErr := cmd.Wait()

	// Wait for background cleanup goroutine to finish.
	if isSignaled(cmd) {
		notifyRunscExited(errRunscNeedsCleanup)
	} else {
		notifyRunscExited(nil)
	}
	// Close the PID file immediately, since runsc has exited and will not
	// be writing to the file anymore.
	// In the case where runsc exits without writing the PID, for example
	// when the binary path is invalid, this allows the <-pidReader.PidChan
	// in the cleanup goroutine to unblock right away.
	pidReader.CloseWriter()
	<-cleanupDone

	// Report errors.
	if errors.Is(cmdErr, exec.ErrWaitDelay) {
		cmdErr = nil
	}
	if cmdErr != nil {
		return errors.Wrap(cmdErr, "")
	}
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

// subCommand returns a command that runs runsc in the sandbox's user and PID namespaces.
func (s *Sandbox) subCommand(args ...string) []string {
	nsArgs := []string{
		nsenterBin, "-t", strconv.Itoa(s.runscPid), "--user", "--pid", "--preserve-credentials",
		setprivBin, "--pdeathsig", "KILL", "--",
		runscBin, "--root", s.state}
	return append(nsArgs, args...)
}

func (s *Sandbox) killInternalProc(ctx context.Context, pid int, sig string) error {
	args := s.subCommand("kill", "--pid", strconv.Itoa(pid), s.id, sig)
	if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
		return errors.Wrapf(err, "runsc kill %s: %s", sig, out)
	}
	return nil
}

var errRunscNeedsCleanup = errors.New("runsc needs cleanup")

func cleanupCtxDone(ctx, runscExited context.Context, kill func(ctx context.Context, pid int, sig string) error, pidC <-chan int) {
	// Wait for ctx to trigger, or when runsc exited unexpectedly.
	select {
	case <-ctx.Done():
	case <-runscExited.Done():
		if context.Cause(runscExited) != errRunscNeedsCleanup {
			return
		}
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get pid from gVisor's kernel.
	var pid int
	select {
	case <-cleanupCtx.Done():
		return
	case p, ok := <-pidC:
		if !ok {
			return
		}
		pid = p
	}

	// Kill pid, and wait till runsc exits.
	kill(cleanupCtx, pid, "KILL")
	select {
	case <-runscExited.Done():
	case <-cleanupCtx.Done():
	}
}

func newCmdCtx(ctx, runscExited context.Context, kill func(ctx context.Context, pid int, sig string) error, pidC <-chan int) (context.Context, <-chan struct{}) {
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer func() {
			cmdCancel()
			close(done)
		}()
		cleanupCtxDone(ctx, runscExited, kill, pidC)
	}()
	return cmdCtx, done
}

func isSignaled(cmd *exec.Cmd) bool {
	if cmd.ProcessState == nil {
		return false
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return ws.Signaled()
}
