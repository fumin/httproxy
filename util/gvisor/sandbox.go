package gvisor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
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

// subCommand returns a command that runs runsc with args in the sandbox's user
// and PID namespaces.
func (s *Sandbox) subCommand(ctx context.Context, args ...string) *exec.Cmd {
	nsArgs := []string{
		"-t", strconv.Itoa(s.runscPid), "--user", "--pid", "--preserve-credentials",
		runscBin, "--root", s.state}
	cmd := exec.CommandContext(ctx, nsenterBin, append(nsArgs, args...)...)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Exec runs the command args in the sandbox until it exits, the sandbox stops,
// or ctx is done. args[0] is looked up in the sandbox's PATH if it contains no
// slash. The command has a terminal if stdin is a terminal.
func (s *Sandbox) Exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := s.subCommand(ctx, append([]string{"exec", s.id}, args...)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// If stdin is not a file, exec.Cmd.Wait would wait for its goroutine
	// copying stdin, which blocks reading stdin even after the shell exits.
	// Thus, copy stdin ourselves without waiting for it.
	// Files, such as terminals, are passed to the shell as is.
	cmd.Stdin = stdin
	if _, ok := stdin.(*os.File); !ok && stdin != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return errors.Wrap(err, "")
		}
		defer func() {
			r.Close()
			w.Close()
		}()
		cmd.Stdin = r

		go func() {
			defer w.Close()
			io.Copy(w, stdin)
		}()
	}

	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}
