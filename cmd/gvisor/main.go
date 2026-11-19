package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/util/gvisor"
)

func mainWithErr() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := gvisor.NewSandbox(ctx, gvisor.DefaultConfig())
	if err != nil {
		return errors.Wrap(err, "")
	}
	execErr := s.Exec(ctx, []string{"/bin/bash"}, os.Stdin, os.Stdout, os.Stderr)
	if err := s.Close(); err != nil {
		return errors.Wrap(err, "")
	}
	if ctx.Err() != nil {
		// We were asked to stop, and the sandbox has stopped.
		return nil
	}
	if execErr != nil {
		return errors.Wrap(execErr, "")
	}
	return nil
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	if err := mainWithErr(); err != nil {
		log.Fatalf("%+v", err)
	}
}
