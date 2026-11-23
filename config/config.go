package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fumin/httproxy/util"
	"github.com/pkg/errors"
)

type Secret struct {
	Password util.Secret[string]
}

type Config struct {
	Secret Secret

	Addr string
	Root string
	DB   string
	XMR  string

	VMRoot             string
	VMRam              uint64
	VMMaxNum           int
	VMUsageGranularity time.Duration
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
		Secret:             secret,
		Addr:               ":28877",
		Root:               filepath.Join(projDir, "data"),
		XMR:                "127.0.0.1:28088",
		VMRam:              1024 << 20,
		VMMaxNum:           2,
		VMUsageGranularity: time.Hour,
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	cfg.VMRoot = filepath.Join(cfg.Root, "ubuntu-24.04")
	return cfg, nil
}

func Dev() (Config, error) {
	const root = "../data"
	secret, err := readSecret(filepath.Join(root, "secret"))
	if err != nil {
		return Config{}, errors.Wrap(err, "")
	}
	cfg := Config{
		Secret:             secret,
		Addr:               ":28877",
		Root:               root,
		XMR:                "127.0.0.1:28088",
		VMRam:              64 << 20,
		VMMaxNum:           2,
		VMUsageGranularity: time.Hour,
	}
	cfg.DB = filepath.Join(cfg.Root, "sqlite.db")
	cfg.VMRoot = filepath.Join(cfg.Root, "ubuntu-24.04")
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
