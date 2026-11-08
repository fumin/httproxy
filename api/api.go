package api

import (
	"net/http"
	"time"

	"github.com/fumin/httproxy/util/jsonrpc"
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
