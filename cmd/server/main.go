package main

import (
	"flag"
	"log"
	"net/http"

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

	s, err := httproxy.NewServer(cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer s.Close()
	log.Printf("Listening on %s", s.Server.Addr)
	if err := s.Server.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("%+v", err)
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
