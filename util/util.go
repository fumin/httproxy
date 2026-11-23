package util

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
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

	secretType := reflect.TypeFor[Secret[string]]()

	// Map keys to fields.
	fields := make(map[string]reflect.Value)
	for i := range sv.NumField() {
		sf := sv.Type().Field(i)
		if !sf.IsExported() {
			continue
		}
		if sf.Type.Kind() != reflect.String && sf.Type != secretType {
			return errors.Errorf("field %s of type %s is not a string or %s", sf.Name, sf.Type, secretType)
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
		val = strings.TrimSpace(val)
		if f.Type() == secretType {
			f.Set(reflect.ValueOf(NewSecret(val)))
		} else {
			f.SetString(val)
		}
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

// A Secret holds a value that prints as [REDACTED] through fmt, log/slog,
// encoding/json, and reflection-based dumpers.
type Secret[T any] struct {
	// A closure rather than a field or pointer: reflection (fmt's fallback
	// for unexported fields, spew, testify diffs) can't look inside a func.
	get func() T
}

// NewSecret creates a secret.
func NewSecret[T any](v T) Secret[T] { return Secret[T]{get: func() T { return v }} }

// Reveal returns the underlying value, or T's zero value for a zero Secret.
func (s Secret[T]) Reveal() T {
	if s.get == nil {
		var zero T
		return zero
	}
	return s.get()
}

func (Secret[T]) Format(f fmt.State, _ rune)   { f.Write([]byte("[REDACTED]")) }
func (Secret[T]) String() string               { return "[REDACTED]" }
func (Secret[T]) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (Secret[T]) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
