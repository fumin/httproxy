package util

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	type secret struct {
		Password          Secret[string]
		LineChannelSecret string
		XMRWallet         string
		Missing           Secret[string]
		unexported        string
	}
	env := `
# comment
Password = p@ss=word
  LineChannelSecret=  secret  
XMRWallet=wallet
unexported=unexported
Unknown=unknown
no equal sign
`
	var s secret
	if err := ParseEnv(&s, strings.NewReader(env)); err != nil {
		t.Fatalf("%+v", err)
	}
	if got := s.Password.Reveal(); got != "p@ss=word" {
		t.Errorf("Password %q", got)
	}
	if s.LineChannelSecret != "secret" {
		t.Errorf("LineChannelSecret %q", s.LineChannelSecret)
	}
	if s.XMRWallet != "wallet" {
		t.Errorf("XMRWallet %q", s.XMRWallet)
	}
	if got := s.Missing.Reveal(); got != "" {
		t.Errorf("Missing %q", got)
	}
	if s.unexported != "" {
		t.Errorf("unexported %q", s.unexported)
	}
	if str := fmt.Sprintf("%+v", s); strings.Contains(str, "p@ss") {
		t.Errorf("leaked secret: %s", str)
	}

	// Unsupported field types are rejected.
	var bad struct{ N int }
	if err := ParseEnv(&bad, strings.NewReader("")); err == nil {
		t.Errorf("expected error")
	}
}

func TestParentPid(t *testing.T) {
	ppid, err := ParentPid(os.Getpid())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if ppid != os.Getppid() {
		t.Fatalf("%d %d", ppid, os.Getppid())
	}

	// A process that does not exist.
	if _, err := ParentPid(-1); err == nil {
		t.Fatalf("expected error")
	}
}
