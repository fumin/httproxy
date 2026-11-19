package gvisor

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/util"
)

// poststartListener learns runsc's PID from runsc's poststart hook, which
// connects to Socket once the container has started.
// https://github.com/opencontainers/runtime-spec/blob/20f948a4a944629335518533250dd719fedd73f2/config.md#poststart
type poststartListener struct {
	ln   *net.UnixListener
	sock string
}

func newPoststartListener(dir string) (*poststartListener, error) {
	sock := filepath.Join(dir, "poststart.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	l := &poststartListener{ln: ln, sock: sock}
	return l, nil
}

func (l *poststartListener) Close() error {
	return l.ln.Close()
}

func (l *poststartListener) Socket() string {
	return l.sock
}

// Listen waits for the hook to connect, and calls started with runsc's PID.
func (l *poststartListener) Listen(ctx context.Context, started func(runscPid int)) error {
	type piderr struct {
		runscPid int
		err      error
	}

	// Run waitPoststart in background.
	c := make(chan piderr, 1)
	// Close listener to guarantee waitPoststart returns when we return.
	defer l.ln.Close()
	go func() {
		runscPid, err := waitPoststart(l.ln)
		c <- piderr{runscPid: runscPid, err: err}
	}()

	// Get result of waitPoststart.
	var res piderr
	select {
	case <-ctx.Done():
		return nil
	case res = <-c:
	}
	if res.err != nil {
		return errors.Wrap(res.err, "")
	}

	started(res.runscPid)
	return nil
}

// waitPoststart waits for runsc's poststart hook to connect to ln, and
// returns the host PID of the `runsc run` process that executed the hook.
func waitPoststart(ln *net.UnixListener) (int, error) {
	conn, err := ln.AcceptUnix()
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	defer conn.Close()

	// Get the peer's PID as seen from our PID namespace.
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	if credErr != nil {
		return -1, errors.Wrap(credErr, "")
	}
	peerPid := int(cred.Pid)

	// Get the PID of runsc, who is the parent process of the hook.
	runscPid, err := util.ParentPid(peerPid)
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(runscPid), "cmdline"))
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	if !bytes.HasPrefix(cmdline, []byte(runscBin+"\x00")) {
		return -1, errors.Errorf("poststart hook %d has parent %d %q, not runsc", cred.Pid, runscPid, cmdline)
	}

	return runscPid, nil
}
