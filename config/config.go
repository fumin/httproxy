package config

import (
	"os"
	"path/filepath"

	"github.com/fumin/httproxy/util"
	"github.com/pkg/errors"
)

// DevRoot is the data directory in development.
const DevRoot = "data"

type Secret struct {
	Password string
}

type Config struct {
	Addr string
	Root string
	DB   string
	XMR  string

	Secret Secret
}

func Prod() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}
	projDir := filepath.Join(home, "var/httproxy")

	secret, err := readSecret(filepath.Join(projDir, "secret"))
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}
	cfg := Config{
		Addr:   ":28877",
		Root:   filepath.Join(projDir, "data"),
		XMR:    "127.0.0.1:28088",
		Secret: secret,
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	return cfg, nil
}

func Dev() (Config, error) {
	secret, err := readSecret("./secret")
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}
	cfg := Config{
		Addr:   ":28877",
		Root:   DevRoot,
		XMR:    "127.0.0.1:28088",
		Secret: secret,
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	return cfg, nil
}

func readSecret(fpath string) (Secret, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return Secret{}, errors.Wrap(err, "")
	}
	defer f.Close()

	var s Secret
	if err := util.ParseEnv(&s, f); err != nil {
		return Secret{}, errors.Wrap(err, "")
	}
	return s, nil
}
