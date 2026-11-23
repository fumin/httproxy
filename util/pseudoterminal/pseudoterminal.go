package pseudoterminal

import (
	"fmt"
	"os"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// Open opens a new pseudoterminal, returning its master and replica.
// The replica can be passed to gvisor.Sandbox.Exec.
func Open() (ptm, pts *os.File, err error) {
	ptm, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, errors.Wrap(err, "")
	}
	var n int
	err = control(ptm, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return errors.Wrap(err, "unlockpt")
		}
		var err error
		n, err = unix.IoctlGetInt(fd, unix.TIOCGPTN)
		return errors.Wrap(err, "ptsname")
	})
	if err != nil {
		ptm.Close()
		return nil, nil, errors.Wrap(err, "")
	}
	pts, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		return nil, nil, errors.Wrap(err, "")
	}
	return ptm, pts, nil
}

// SetWinsize sets the window size of the pseudoterminal whose master is ptm.
func SetWinsize(ptm *os.File, width, height uint16) error {
	return control(ptm, func(fd int) error {
		ws := &unix.Winsize{Col: width, Row: height}
		return errors.Wrap(unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws), "")
	})
}

// control runs fn on f's file descriptor.
// Unlike f.Fd, it keeps f non-blocking, so that f's deadlines keep working.
func control(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return errors.Wrap(err, "")
	}
	var fnErr error
	if err := rc.Control(func(fd uintptr) { fnErr = fn(int(fd)) }); err != nil {
		return errors.Wrap(err, "")
	}
	return fnErr
}
