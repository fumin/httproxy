package api

import (
	"github.com/fumin/httproxy/util/jsonrpc"
)

const (
	ProxyPriceXMRPicoPerByte = 20
)

type Client struct {
	url string
}

func NewClient(host string) *Client {
	return &Client{url: "http://" + host + "/json_rpc"}
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
	UsageLimit int64
	Usage      int64
}

func (c *Client) Topup(in TopupIn) (out Session, err error) {
	err = jsonrpc.Do(c.url, "topup", in, &out, nil)
	return
}

type UsageIn struct {
	SessionID string
}

func (c *Client) Usage(in UsageIn) (out Session, err error) {
	err = jsonrpc.Do(c.url, "usage", in, &out, nil)
	return
}
