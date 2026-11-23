package api

import (
	"github.com/fumin/httproxy/util/jsonrpc"
)

const (
	ProxyPriceXMRPicoPerByte = 10
	VMPriceXMRPicoPerHour    = 15000000
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

type ProxyTopupIn struct {
	Method    PaymentMethod
	PaymentTx string
	SessionID string
}

type Session struct {
	ID         string
	UsageLimit int64
	Usage      int64
}

func (c *Client) ProxyTopup(in ProxyTopupIn) (out Session, err error) {
	err = jsonrpc.Do(c.url, "ProxyTopup", in, &out, nil)
	return
}

type ProxyUsageIn struct {
	SessionID string
}

func (c *Client) ProxyUsage(in ProxyUsageIn) (out Session, err error) {
	err = jsonrpc.Do(c.url, "ProxyUsage", in, &out, nil)
	return
}

type VMTopupIn struct {
	Method    PaymentMethod
	PaymentTx string
	ID        string
}

type VM struct {
	ID         string
	UsageLimit int
	Usage      int
	Running    bool
}

func (c *Client) VMTopup(in VMTopupIn) (out VM, err error) {
	err = jsonrpc.Do(c.url, "VMTopup", in, &out, nil)
	return
}

type VMInfoIn struct {
	ID string
}

func (c *Client) VMInfo(in VMInfoIn) (out VM, err error) {
	err = jsonrpc.Do(c.url, "VMInfo", in, &out, nil)
	return
}
