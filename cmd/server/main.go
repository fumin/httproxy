package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy"
	"github.com/fumin/httproxy/config"
)

var (
	cfgName = flag.String("c", "dev", "configuration name")
)

func mainWithErr() error {
	// Read config.
	var cfg config.Config
	var err error
	switch *cfgName {
	case "prod":
		cfg, err = config.Prod()
	default:
		cfg, err = config.Dev()
	}
	if err != nil {
		return errors.Wrap(err, "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := httproxy.NewServer(ctx, cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer s.Close()

	// Start server.
	stopCtx, revertSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer revertSignalHandling()
	go func() {
		<-stopCtx.Done()
		revertSignalHandling()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Server.Shutdown(ctx); err != nil {
			log.Printf("%+v", err)
		}
	}()
	log.Printf("Listening on %s", s.Server.Addr)
	if err := s.Server.ListenAndServe(); err != http.ErrServerClosed {
		return errors.Wrap(err, "")
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
