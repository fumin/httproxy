package gvisor

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSandbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	s, err := NewSandbox(ctx, DefaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}()

	// Both shells are running at the same time.
	term0 := execTerminal(ctx, s)
	term1 := execTerminal(ctx, s)

	if _, err := io.WriteString(term0.stdin, "echo foo > /tmp/bar\n"); err != nil {
		t.Fatalf("%+v", err)
	}
	if err := term0.Close(); err != nil {
		t.Fatalf("%+v %s", err, term0.stderr.String())
	}

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
	s, err := NewSandbox(ctx, DefaultConfig())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	term := execTerminal(context.Background(), s)

	// stopTimeout is how long tests wait for a sandbox or shell to stop.
	const stopTimeout = 10 * time.Second

	cancel()
	select {
	case <-s.done:
	case <-time.After(stopTimeout):
		t.Fatalf("sandbox still running after ctx is done")
	}
	if err := s.Close(); err != nil {
		t.Errorf("%+v", err)
	}
	// Shells started by Exec stop with the sandbox.
	select {
	case <-term.done:
	case <-time.After(stopTimeout):
		t.Fatalf("shell still running after sandbox stopped")
	}
}

func TestSandboxExecArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSandbox(ctx, DefaultConfig())
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
		if err := s.Exec(ctx, test.args, nil, &stdout, &stderr); err != nil {
			t.Fatalf("%+v %q", err, stderr.String())
		}
		if got := stdout.String(); got != test.want {
			t.Errorf("%q: got %q, want %q", test.args, got, test.want)
		}
	}

	// A failing command returns its exit status.
	err = s.Exec(ctx, []string{"/bin/sh", "-c", "exit 3"}, nil, io.Discard, io.Discard)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("got %+v, want exit status 3", err)
	}

	if err := s.Exec(ctx, nil, nil, io.Discard, io.Discard); err == nil {
		t.Errorf("no error for empty args")
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	m.Run()
}

// terminal is a shell started by Sandbox.Exec.
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
		t.done <- s.Exec(ctx, []string{"/bin/bash"}, stdinR, &t.stdout, &t.stderr)
	}()
	return t
}

// Close ends the shell's input and waits for it to exit.
func (t *terminal) Close() error {
	t.stdin.Close()
	return <-t.done
}
