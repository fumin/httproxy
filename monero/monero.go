package monero

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

func ToPico(amount float64) int {
	return int(amount * 1e12)
}

func FromPico(amount int) float64 {
	return float64(amount) * 1e-12
}

type Wallet struct {
	url    string
	client *http.Client
}

func NewWallet(host string) *Wallet {
	return &Wallet{url: "http://" + host + "/json_rpc", client: &http.Client{}}
}

type GetBalanceReq struct {
	AccountIndex int `json:"account_index"`
}

type GetBalanceResp struct {
	Balance        int `json:"balance"`
	BlocksToUnlock int `json:"blocks_to_unlock"`
	PerSubaddress  []struct {
		AccountIndex      int    `json:"account_index"`
		Address           string `json:"address"`
		AddressIndex      int    `json:"address_index"`
		Balance           int    `json:"balance"`
		BlocksToUnlock    int    `json:"blocks_to_unlock"`
		Label             string `json:"label"`
		NumUnspentOutputs int    `json:"num_unspent_outputs"`
		TimeToUnlock      int    `json:"time_to_unlock"`
		UnlockedBalance   int    `json:"unlocked_balance"`
	} `json:"per_subaddress"`
	TimeToUnlock    int `json:"time_to_unlock"`
	UnlockedBalance int `json:"unlocked_balance"`
}

type GetAddressReq struct {
	AccountIndex int `json:"account_index"`
}

type GetAddressResp struct {
	Address   string `json:"address"`
	Addresses []struct {
		Address      string `json:"address"`
		AddressIndex int    `json:"address_index"`
		Label        string `json:"label"`
		Used         bool   `json:"used"`
	} `json:"addresses"`
}

func (c *Wallet) GetAddress(req GetAddressReq) (GetAddressResp, error) {
	var resp GetAddressResp
	if err := do(c.client, c.url, "get_address", req, &resp); err != nil {
		return GetAddressResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

func (c *Wallet) GetBalance(req GetBalanceReq) (GetBalanceResp, error) {
	var resp GetBalanceResp
	if err := do(c.client, c.url, "get_balance", req, &resp); err != nil {
		return GetBalanceResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

type Destination struct {
	Amount  int    `json:"amount"`
	Address string `json:"address"`
}

type TransferReq struct {
	AccountIndex int           `json:"account_index"`
	Destinations []Destination `json:"destinations"`
	Priority     int           `json:"priority"`
	UnlockTime   int           `json:"unlock_time"`
}

type TransferResp struct {
	Amount int    `json:"amount"`
	Fee    int    `json:"fee"`
	TxHash string `json:"tx_hash"`
}

func (c *Wallet) Transfer(req TransferReq) (TransferResp, error) {
	var resp TransferResp
	if err := do(c.client, c.url, "transfer", req, &resp); err != nil {
		return TransferResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

type RefreshReq struct {
	StartHeight int `json:"start_height,omitempty"`
}

type RefreshResp struct {
	BlocksFetched int  `json:"blocks_fetched"`
	ReceivedMoney bool `json:"received_money"`
}

func (c *Wallet) Refresh(req RefreshReq) (RefreshResp, error) {
	var resp RefreshResp
	if err := do(c.client, c.url, "refresh", req, &resp); err != nil {
		return RefreshResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

type GetTransferByTxidReq struct {
	Txid string `json:"txid"`
}

type GetTransferByTxidResp struct {
	Transfers []struct {
		Address                         string `json:"address"`
		Amount                          int    `json:"amount"`
		Confirmations                   int    `json:"confirmations"`
		DoubleSpendSeen                 bool   `json:"double_spend_seen"`
		Fee                             int    `json:"fee"`
		Height                          int    `json:"height"`
		Locked                          bool   `json:"locked"`
		Note                            string `json:"note"`
		SuggestedConfirmationsThreshold int    `json:"suggested_confirmations_threshold"`
		Timestamp                       int    `json:"timestamp"`
		Txid                            string `json:"txid"`
		Type                            string `json:"type"`
		UnlockTime                      int    `json:"unlock_time"`
	} `json:"transfers"`
}

func (c *Wallet) GetTransferByTxid(req GetTransferByTxidReq) (GetTransferByTxidResp, error) {
	var resp GetTransferByTxidResp
	if err := do(c.client, c.url, "get_transfer_by_txid", req, &resp); err != nil {
		return GetTransferByTxidResp{}, errors.Wrap(err, "")
	}
	return resp, nil
}

type request struct {
	ID      string `json:"id"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e RPCError) Error() string {
	return e.Message
}

type response struct {
	Error  RPCError `json:"error"`
	Result any      `json:"result"`
}

func do(client *http.Client, url, method string, params, result any) error {
	// Prepare request body.
	reqJ := request{ID: "0", JSONRPC: "2.0", Method: method, Params: params}
	reqBody := new(bytes.Buffer)
	if err := json.MarshalWrite(reqBody, reqJ); err != nil {
		return errors.Wrap(err, "")
	}

	// Do request.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return errors.Wrap(err, "")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer resp.Body.Close()

	// Parse response.
	respJ := response{Result: result}
	if err := json.UnmarshalRead(resp.Body, &respJ); err != nil {
		return errors.Wrap(err, "")
	}
	if respJ.Error.Code != 0 {
		return respJ.Error
	}
	return nil
}
