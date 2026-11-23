package gvisor

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestSandbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Create sandbox and terminals.
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}()
	term0 := execTerminal(ctx, s)
	term1 := execTerminal(ctx, s)

	// term0 writes.
	if _, err := io.WriteString(term0.stdin, "echo foo > /tmp/bar\n"); err != nil {
		t.Fatalf("%+v", err)
	}
	if err := term0.Close(); err != nil {
		t.Fatalf("%+v %s", err, term0.stderr.String())
	}

	// term1 reads what term0 writes.
	if _, err := io.WriteString(term1.stdin, "cat /tmp/bar\n"); err != nil {
		t.Fatalf("%+v", err)
	}
	if err := term1.Close(); err != nil {
		t.Fatalf("%+v %s", err, term1.stderr.String())
	}

	if got := term1.stdout.String(); got != "foo\n" {
		t.Errorf("got %q, want %q, stderr %q", got, "foo\n", term1.stderr.String())
	}
}

func TestSandboxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	term := execTerminal(context.Background(), s)

	const stopTimeout = 10 * time.Second

	// Check that sandbox is closed by ctx.
	cancel()
	select {
	case <-s.done:
	case <-time.After(stopTimeout):
		t.Fatalf("sandbox still running after ctx is done")
	}
	if err := s.Close(); err != nil {
		t.Errorf("%+v", err)
	}

	// Check that Exec is stopped when sandbox closes.
	select {
	case <-term.done:
	case <-time.After(stopTimeout):
		t.Fatalf("shell still running after sandbox stopped")
	}
}

func TestSandboxRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := defaultConfig()
	// Run as root, so that anywhere in Root can be written.
	cfg.User = specs.User{}
	s, err := NewSandbox(ctx, cfg)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer s.Close()

	// The sandbox sees Root, with bind Mounts on top.
	want := map[string]string{"/etc/os-release": readFile(t, filepath.Join(cfg.Root, "etc", "os-release"))}
	for _, m := range cfg.Mounts {
		if m.Type == "bind" {
			want[m.Destination] = readFile(t, m.Source)
		}
	}
	for path, want := range want {
		var stdout, stderr strings.Builder
		if err := s.Exec(ctx, []string{"cat", path}, nil, &stdout, &stderr, nil); err != nil {
			t.Fatalf("%+v %q", err, stderr.String())
		}
		if got := stdout.String(); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}

	// Writes inside the sandbox do not reach Root on the host.
	name := "gvisor-test-" + strconv.Itoa(os.Getpid())
	if err := s.Exec(ctx, []string{"touch", "/" + name}, nil, io.Discard, io.Discard, nil); err != nil {
		t.Fatalf("%+v", err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.Root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s written to host Root: %v", name, err)
	}
}

func TestSandboxExecBash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer s.Close()

	tests := []struct {
		args []string
		want string
	}{
		// Arguments are passed as is, even those that look like flags.
		{args: []string{"/usr/bin/printf", "%s|", "a b", "-c", "--root", "'\"$HOME"}, want: `a b|-c|--root|'"$HOME|`},
		// Commands are looked up in PATH.
		{args: []string{"echo", "foo"}, want: "foo\n"},
	}
	for _, test := range tests {
		var stdout, stderr strings.Builder
		if err := s.Exec(ctx, test.args, nil, &stdout, &stderr, nil); err != nil {
			t.Fatalf("%+v %q", err, stderr.String())
		}
		if got := stdout.String(); got != test.want {
			t.Errorf("%q: got %q, want %q", test.args, got, test.want)
		}
	}

	// A failing command returns its exit status.
	err = s.Exec(ctx, []string{"/bin/sh", "-c", "exit 3"}, nil, io.Discard, io.Discard, nil)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("got %+v, want exit status 3", err)
	}
}

func TestSandboxExecCancelEarly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer s.Close()

	for i, d := range []int{1, 2, 5, 10, 15, 20, 25, 30, 40, 60} {
		start := time.Now()
		err := func() error {
			// execCtx timeout almost immediately.
			execCtx, execCancel := context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
			defer execCancel()
			return s.Exec(execCtx, []string{"sleep", strconv.Itoa(1000 + i)}, nil, io.Discard, io.Discard, nil)
		}()

		// Check that we got an error, and we did not hang.
		if err == nil {
			t.Errorf("cancel after %dms: no error", d)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("cancel after %dms: Exec took %v", d, took)
		}
	}
	if p := sandboxProcs(ctx, t, s); strings.Contains(p, "sleep 10") {
		t.Errorf("cancelled commands still running:\n%s", p)
	}
}

func TestSandboxExecCancelKill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer s.Close()

	start := time.Now()
	script := "sleep 2001 & " +
		"trap '' HUP INT TERM; " + // ignore SIGHUP, SIGINT and SIGTERM
		"while :; do sleep 0.1; done"
	err = func() error {
		execCtx, execCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer execCancel()
		return s.Exec(execCtx, []string{"/bin/sh", "-c", script}, nil, io.Discard, io.Discard, nil)
	}()
	// Check that we got an error, and we did not hang.
	if err == nil {
		t.Errorf("should get error")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Exec took %v", took)
	}

	// Check that the shell is killed, but its child process continues running.
	p := sandboxProcs(ctx, t, s)
	if strings.Contains(p, "/bin/sh") {
		t.Errorf("cancelled command still running:\n%s", p)
	}
	if !strings.Contains(p, "sleep 2001") {
		t.Errorf("process started by the command killed:\n%s", p)
	}
}

func TestSandboxExecRunscKilled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSandbox(ctx, defaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer s.Close()

	done := make(chan error, 1)
	go func() {
		done <- s.Exec(ctx, []string{"sleep", "4001"}, nil, io.Discard, io.Discard, nil)
	}()

	// Get the host pid of runsc, and the sandbox pid of sleep.
	var runscPid, sleepPid int
	for start := time.Now(); (runscPid == 0 || sleepPid == 0) && time.Since(start) < 5*time.Second; {
		out, _ := exec.Command("pgrep", "-n", "-f", "^"+runscBin+" .* sleep 4001$").Output()
		runscPid, _ = strconv.Atoi(strings.TrimSpace(string(out)))
		var sout strings.Builder
		s.Exec(ctx, []string{"pgrep", "-x", "-f", "sleep 4001"}, nil, &sout, io.Discard, nil)
		sleepPid, _ = strconv.Atoi(strings.TrimSpace(sout.String()))
		time.Sleep(200 * time.Millisecond)
	}
	if runscPid == 0 || sleepPid == 0 {
		t.Fatalf("processes not found %d %d", runscPid, sleepPid)
	}

	// Kill runsc, and check that Exec returns quickly.
	if err := syscall.Kill(runscPid, syscall.SIGKILL); err != nil {
		t.Fatalf("%+v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("should get error")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Exec still running after runsc was killed")
	}

	// Check that process inside gVisor is killed as well.
	var out strings.Builder
	s.Exec(ctx, []string{"ps", "-o", "args=", "-p", strconv.Itoa(sleepPid)}, nil, &out, &out, nil)
	if p := strings.TrimSpace(out.String()); p != "" {
		t.Errorf("command still running after runsc was killed: pid %d: %s", sleepPid, p)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	m.Run()
}

func defaultConfig() Config {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "data", "ubuntu-24.04"))
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	return DefaultConfig(root)
}

type terminal struct {
	stdin  *io.PipeWriter
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   chan error
}

func execTerminal(ctx context.Context, s *Sandbox) *terminal {
	stdinR, stdinW := io.Pipe()
	t := &terminal{stdin: stdinW, done: make(chan error, 1)}
	go func() {
		t.done <- s.Exec(ctx, []string{"/bin/bash"}, stdinR, &t.stdout, &t.stderr, nil)
	}()
	return t
}

func (t *terminal) Close() error {
	t.stdin.Close()
	return <-t.done
}

// sandboxProcs returns the command lines of the processes running in s.
func sandboxProcs(ctx context.Context, t *testing.T, s *Sandbox) string {
	t.Helper()
	var out strings.Builder
	if err := s.Exec(ctx, []string{"ps", "-eo", "args"}, nil, &out, &out, nil); err != nil {
		t.Fatalf("%+v %s", err, out.String())
	}
	return out.String()
}

func readFile(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	return string(b)
}
