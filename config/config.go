package config

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

type Config struct {
	Addr string
	Root string
	DB   string
	XMR  string
}

func Prod() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}

	cfg := Config{
		Addr: ":28877",
		Root: filepath.Join(home, "var/httproxy/data"),
		XMR:  "127.0.0.1:28088",
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	return cfg, nil
}

func Dev() Config {
	cfg := Config{
		Addr: ":28877",
		Root: "data",
		XMR:  "127.0.0.1:28088",
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	return cfg
}
