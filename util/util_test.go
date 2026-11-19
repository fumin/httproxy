package util

import (
	"os"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	type secret struct {
		Password          string
		LineChannelSecret string
		XMRWallet         string
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
	expected := secret{
		Password:          "p@ss=word",
		LineChannelSecret: "secret",
		XMRWallet:         "wallet",
	}
	if s != expected {
		t.Errorf("%+v", s)
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
