package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fumin/httproxy/util/nsjail"
)

func mainWithErr() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return nsjail.Run(ctx, os.Stdin, os.Stdout, os.Stderr)
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	if err := mainWithErr(); err != nil {
		log.Fatalf("%+v", err)
	}
}
