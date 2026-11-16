package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fumin/httproxy/util/gvisor"
)

func mainWithErr() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return gvisor.Run(ctx, os.Stdin, os.Stdout, os.Stderr)
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	if err := mainWithErr(); err != nil {
		log.Fatalf("%+v", err)
	}
}
