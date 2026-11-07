package monero

import (
	"net/http"

	"github.com/pkg/errors"
)

type Node struct {
	url    string
	client *http.Client
}

func NewNode(host string) (*Node, error) {
	n := &Node{url: "http://" + host + "/json_rpc", client: &http.Client{}}
	return n, nil
}

type GenerateblocksReq struct {
	AmountOfBlocks int    `json:"amount_of_blocks"`
	WalletAddress  string `json:"wallet_address"`
}

type GenerateblocksResp struct {
	Blocks    []string `json:"blocks"`
	Height    int      `json:"height"`
	Status    string   `json:"status"`
	Untrusted bool     `json:"untrusted"`
}

func (n *Node) Generateblocks(req GenerateblocksReq) (GenerateblocksResp, error) {
	var resp GenerateblocksResp
	if err := do(n.client, n.url, "generateblocks", req, &resp); err != nil {
		return GenerateblocksResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

type GetFeeEstimateReq struct {
	GraceBlocks int `json:"grace_blocks"`
}

type GetFeeEstimateResp struct {
	Fee              int    `json:"fee"`
	QuantizationMask int    `json:"quantization_mask"`
	Status           string `json:"status"`
	Untrusted        bool   `json:"untrusted"`
}

func (n *Node) GetFeeEstimate(req GetFeeEstimateReq) (GetFeeEstimateResp, error) {
	var resp GetFeeEstimateResp
	if err := do(n.client, n.url, "get_fee_estimate", req, &resp); err != nil {
		return GetFeeEstimateResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}
