package httproxy

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/fumin/httproxy/util/jsonrpc"
)

func TestDealVoucher(t *testing.T) {
	b := newBackend(t)
	t.Cleanup(b.Close)
	rpcURL := "http://" + b.hs.Listener.Addr().String() + "/admin/json_rpc"

	tests := []struct {
		token string
		ok    bool
	}{
		{token: b.s.auth.Token(), ok: true},
		{token: "wrongtoken", ok: false},
	}
	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Parallel()
			header := http.Header{}
			header.Set("Authorization", "Bearer "+test.token)
			var ss Session
			err := jsonrpc.Do(rpcURL, "deal_voucher", struct{}{}, &ss, header)
			if (err == nil) != test.ok {
				t.Errorf("%+v", err)
			}
		})
	}
}
