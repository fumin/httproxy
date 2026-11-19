package gvisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/fumin/httproxy/util"
	"github.com/pkg/errors"
)

// internalPidReader learns the PID inside the sandbox of a command started by
// `runsc exec --internal-pid-file`, which runsc writes right after the command
// starts. The file is a FIFO, so that the PID is received as soon as runsc
// writes it, without polling.
type internalPidReader struct {
	// PidChan receives the PID once runsc writes it, and is then closed.
	// It is closed without receiving anything if no valid PID is written
	// before CloseWriter or Close.
	PidChan chan int

	pidFile *os.File
	// writer is a write end of the FIFO, which keeps reading pidFile from
	// seeing EOF before runsc opens the FIFO.
	writer *os.File
	// done is closed once the goroutine reading pidFile returns.
	done chan struct{}
}

// newInternalPidReader creates the FIFO with a random name inside dir, which
// should be writable only by us. Mkfifo fails rather than reuse an existing
// file of the same name.
func newInternalPidReader(dir string) (*internalPidReader, error) {
	path := filepath.Join(dir, "exec"+util.RandStr())
	if err := syscall.Mkfifo(path, 0600); err != nil {
		return nil, errors.Wrap(err, "")
	}
	// Opening the read end non-blocking does not wait for a writer, and
	// opening a write end then does not block either, since the read end is
	// open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		os.Remove(path)
		return nil, errors.Wrap(err, "")
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, errors.Wrap(err, "")
	}

	r := &internalPidReader{PidChan: make(chan int, 1), pidFile: f, writer: w, done: make(chan struct{})}
	go r.read()
	return r, nil
}

// Path returns the file to pass to `runsc exec --internal-pid-file`.
func (r *internalPidReader) Path() string {
	return r.pidFile.Name()
}

func (r *internalPidReader) read() {
	defer close(r.done)
	defer close(r.PidChan)

	// The PID is a few bytes, far below PIPE_BUF, which runsc writes at
	// once, so a single Read gets all of it. Read sees EOF once no write end
	// is open and nothing is left to read, such as after runsc exits and
	// CloseWriter closes ours, and fails once Close closes pidFile.
	buf := make([]byte, 32)
	n, err := r.pidFile.Read(buf)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return
	}
	r.PidChan <- pid
}

// CloseWriter tells r that runsc has exited, so that PidChan receives the PID
// if runsc wrote one, and is closed without waiting any longer otherwise.
func (r *internalPidReader) CloseWriter() {
	r.writer.Close()
}

// Close stops waiting for the PID, and removes the FIFO.
func (r *internalPidReader) Close() error {
	r.writer.Close()
	// Closing pidFile makes a pending Read return.
	err := r.pidFile.Close()
	<-r.done
	if rmErr := os.Remove(r.pidFile.Name()); err == nil {
		err = rmErr
	}
	return errors.Wrap(err, "")
}
