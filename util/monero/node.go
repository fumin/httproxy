package monero

import (
	"net/http"

	"github.com/fumin/httproxy/util/jsonrpc"
)

type Node struct {
	url    string
	client *http.Client
}

func NewNode(host string) (*Node, error) {
	n := &Node{url: "http://" + host + "/json_rpc", client: &http.Client{}}
	return n, nil
}

type GenerateblocksIn struct {
	AmountOfBlocks int    `json:"amount_of_blocks"`
	WalletAddress  string `json:"wallet_address"`
}

type GenerateblocksOut struct {
	Blocks    []string `json:"blocks"`
	Height    int      `json:"height"`
	Status    string   `json:"status"`
	Untrusted bool     `json:"untrusted"`
}

func (n *Node) Generateblocks(in GenerateblocksIn) (out GenerateblocksOut, err error) {
	err = jsonrpc.Do(n.client, n.url, "generateblocks", in, &out)
	return
}

type GetFeeEstimateIn struct {
	GraceBlocks int `json:"grace_blocks"`
}

type GetFeeEstimateOut struct {
	Fee              Piconero   `json:"fee"`
	Fees             []Piconero `json:"fees"`
	QuantizationMask Piconero   `json:"quantization_mask"`
	Status           string     `json:"status"`
	Untrusted        bool       `json:"untrusted"`
}

func (n *Node) GetFeeEstimate(in GetFeeEstimateIn) (out GetFeeEstimateOut, err error) {
	err = jsonrpc.Do(n.client, n.url, "get_fee_estimate", in, &out)
	return
}
