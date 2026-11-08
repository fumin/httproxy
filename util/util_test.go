package util

import (
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
