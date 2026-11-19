package util

import (
	"bufio"
	"crypto/rand"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/address/v2/base58"
	"github.com/pkg/errors"
)

func RandStr() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	a, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	c := alphabet[a.Int64()]

	b := make([]byte, 64/8)
	rand.Read(b)
	s := base58.Encode(b)

	return string(c) + s
}

func WriteTemp(pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", errors.Wrap(err, "")
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", errors.Wrap(err, "")
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", errors.Wrap(err, "")
	}
	return name, nil
}

func ParseEnv(v any, r io.Reader) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return errors.Errorf("%T is not a non-nil pointer to a struct", v)
	}
	sv := rv.Elem()

	// Map keys to fields.
	fields := make(map[string]reflect.Value)
	for i := range sv.NumField() {
		sf := sv.Type().Field(i)
		if !sf.IsExported() {
			continue
		}
		if sf.Type.Kind() != reflect.String {
			return errors.Errorf("field %s of type %s is not a string", sf.Name, sf.Type)
		}
		fields[sf.Name] = sv.Field(i)
	}

	// Parse lines.
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		f, ok := fields[strings.TrimSpace(k)]
		if !ok {
			continue
		}
		f.SetString(strings.TrimSpace(val))
	}
	if err := scanner.Err(); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

// ParentPid returns the PID of pid's parent.
func ParentPid(pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, errors.Wrap(err, "")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			ppid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, errors.Wrap(err, line)
			}
			return ppid, nil
		}
	}
	return 0, errors.Errorf("no PPid in %s", b)
}
