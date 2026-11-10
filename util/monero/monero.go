package monero

import (
	"github.com/fumin/httproxy/util/jsonrpc"
)

type Piconero int

func (p Piconero) XMRs() float64 {
	return float64(p) * 1e-12
}

func Picos(amount float64) Piconero {
	return Piconero(amount * 1e12)
}

type Wallet struct {
	url string
}

func NewWallet(host string) *Wallet {
	return &Wallet{url: "http://" + host + "/json_rpc"}
}

type GetBalanceIn struct {
	AccountIndex int `json:"account_index"`
}

type GetBalanceOut struct {
	Balance        Piconero `json:"balance"`
	BlocksToUnlock int      `json:"blocks_to_unlock"`
	PerSubaddress  []struct {
		AccountIndex      int      `json:"account_index"`
		Address           string   `json:"address"`
		AddressIndex      int      `json:"address_index"`
		Balance           Piconero `json:"balance"`
		BlocksToUnlock    int      `json:"blocks_to_unlock"`
		Label             string   `json:"label"`
		NumUnspentOutputs int      `json:"num_unspent_outputs"`
		TimeToUnlock      int      `json:"time_to_unlock"`
		UnlockedBalance   Piconero `json:"unlocked_balance"`
	} `json:"per_subaddress"`
	TimeToUnlock    int      `json:"time_to_unlock"`
	UnlockedBalance Piconero `json:"unlocked_balance"`
}

func (c *Wallet) GetBalance(in GetBalanceIn) (out GetBalanceOut, err error) {
	err = jsonrpc.Do(c.url, "get_balance", in, &out, nil)
	return
}

type GetAddressIn struct {
	AccountIndex int   `json:"account_index"`
	AddressIndex []int `json:"address_index,omitzero"`
}

type GetAddressOut struct {
	Address   string `json:"address"`
	Addresses []struct {
		Address      string `json:"address"`
		AddressIndex int    `json:"address_index"`
		Label        string `json:"label"`
		Used         bool   `json:"used"`
	} `json:"addresses"`
}

func (c *Wallet) GetAddress(in GetAddressIn) (out GetAddressOut, err error) {
	err = jsonrpc.Do(c.url, "get_address", in, &out, nil)
	return
}

type Destination struct {
	Amount  Piconero `json:"amount"`
	Address string   `json:"address"`
}

type TransferIn struct {
	AccountIndex int           `json:"account_index"`
	Destinations []Destination `json:"destinations"`
	Priority     int           `json:"priority"`
	UnlockTime   int           `json:"unlock_time"`
}

type TransferOut struct {
	Amount Piconero `json:"amount"`
	Fee    Piconero `json:"fee"`
	TxHash string   `json:"tx_hash"`
}

func (c *Wallet) Transfer(in TransferIn) (out TransferOut, err error) {
	err = jsonrpc.Do(c.url, "transfer", in, &out, nil)
	return
}

type RefreshIn struct {
	StartHeight int `json:"start_height,omitempty"`
}

type RefreshOut struct {
	BlocksFetched int  `json:"blocks_fetched"`
	ReceivedMoney bool `json:"received_money"`
}

func (c *Wallet) Refresh(in RefreshIn) (out RefreshOut, err error) {
	err = jsonrpc.Do(c.url, "refresh", in, &out, nil)
	return
}

type GetTransferByTxidIn struct {
	Txid string `json:"txid"`
}

type TxType string

const (
	TxTypeIn      TxType = "in"
	TxTypeOut            = "out"
	TxTypePool           = "pool"
	TxTypePending        = "pending"
	TxTypeFailed         = "failed"
)

type GetTransferByTxidOut struct {
	Transfers []struct {
		Address                         string   `json:"address"`
		Amount                          Piconero `json:"amount"`
		Confirmations                   int      `json:"confirmations"`
		DoubleSpendSeen                 bool     `json:"double_spend_seen"`
		Fee                             Piconero `json:"fee"`
		Height                          int      `json:"height"`
		Locked                          bool     `json:"locked"`
		Note                            string   `json:"note"`
		SuggestedConfirmationsThreshold int      `json:"suggested_confirmations_threshold"`
		Timestamp                       int      `json:"timestamp"`
		Txid                            string   `json:"txid"`
		Type                            TxType   `json:"type"`
		UnlockTime                      int      `json:"unlock_time"`
	} `json:"transfers"`
}

func (c *Wallet) GetTransferByTxid(in GetTransferByTxidIn) (out GetTransferByTxidOut, err error) {
	err = jsonrpc.Do(c.url, "get_transfer_by_txid", in, &out, nil)
	return
}
