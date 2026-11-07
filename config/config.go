package config

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

type Config struct {
	Addr string
	Root string
}

func Prod() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}

	cfg := Config{
		Addr: ":28877",
		Root: filepath.Join(home, "var/httproxy/data"),
	}
	return cfg, nil
}

func Dev() (Config, error) {
	cfg := Config{
		Addr: ":28877",
		Root: "data",
	}
	return cfg, nil
}
