package client

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"time"

	"github.com/fumin/httproxy/util/jsonrpc"
	"github.com/pkg/errors"
)

const (
	ProxyPriceXMRPiconeroPerSecond = 5347 // 3e-6 USD/second
)

type Client struct {
	url    string
	client *http.Client
}

func NewClient(host string) *Client {
	return &Client{url: "http://" + host + "/json_rpc", client: &http.Client{}}
}

type PaymentMethod string

const (
	MethodXMR PaymentMethod = "xmr"
)

type TopupIn struct {
	Method    PaymentMethod
	PaymentTx string
	SessionID string
}

type Session struct {
	ID         string
	UsageLimit time.Duration
	Usage      time.Duration
}

func (s *Session) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var sj struct {
		ID         string
		UsageLimit int64
		Usage      int64
	}
	if err := json.UnmarshalDecode(dec, &sj); err != nil {
		return errors.Wrap(err, "")
	}
	s.ID = sj.ID
	s.UsageLimit = time.Duration(sj.UsageLimit) * time.Second
	s.Usage = time.Duration(sj.Usage) * time.Second
	return nil
}

func (c *Client) Topup(in TopupIn) (out Session, err error) {
	err = jsonrpc.Do(c.client, c.url, "topup", in, &out)
	return
}

type UsageIn struct {
	SessionID string
}

func (c *Client) Usage(in UsageIn) (out Session, err error) {
	err = jsonrpc.Do(c.client, c.url, "usage", in, &out)
	return
}
