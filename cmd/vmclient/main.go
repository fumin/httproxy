// Command vmclient opens a login shell in a VM, using the Kubernetes
// client-go implementation of the WebSocket remote command protocol,
// served by httproxy's /VMLogin.
package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/term"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

var (
	addr      = flag.String("url", "http://localhost:28877/VMLogin", "URL of /VMLogin")
	id        = flag.String("id", "", "ID of the VM to log in to, sent as a Bearer token")
	protocols = flag.String("protocol", "v5.channel.k8s.io,v4.channel.k8s.io", "comma separated subprotocols to offer")
)

// sizeQueue reports the size of a terminal, initially and after every resize.
type sizeQueue struct {
	fd    int
	winch chan os.Signal
}

func newSizeQueue(fd int) *sizeQueue {
	q := &sizeQueue{fd: fd, winch: make(chan os.Signal, 1)}
	signal.Notify(q.winch, syscall.SIGWINCH)
	q.winch <- syscall.SIGWINCH
	return q
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	for range q.winch {
		if w, h, err := term.GetSize(q.fd); err == nil {
			return &remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}
		}
	}
	return nil
}

func mainWithErr() error {
	if *id == "" {
		return errors.Errorf("-id is required")
	}
	u, err := url.Parse(*addr)
	if err != nil {
		return errors.Wrap(err, "")
	}
	// The VM ID is sent in the "Authorization: Bearer" header.
	cfg := &rest.Config{Host: u.Scheme + "://" + u.Host, BearerToken: *id}
	ex, err := remotecommand.NewWebSocketExecutorForProtocols(cfg, "GET", u.String(), strings.Split(*protocols, ",")...)
	if err != nil {
		return errors.Wrap(err, "")
	}

	// Raw mode so keystrokes such as Ctrl-C go to the remote shell, as with a real terminal.
	opts := remotecommand.StreamOptions{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		// With a TTY, stderr is merged into stdout, so leave Stderr nil.
		Tty: true,
	}
	fd := int(os.Stdin.Fd())
	if old, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, old)
		opts.TerminalSizeQueue = newSizeQueue(fd)
	}

	if err := ex.StreamWithContext(context.Background(), opts); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	// mainWithErr has restored the terminal by the time it returns.
	err := mainWithErr()
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		os.Stderr.WriteString(exitErr.Error() + "\n")
		os.Exit(exitErr.ExitStatus())
	}
	if err != nil {
		log.Fatalf("%+v", err)
	}
}
