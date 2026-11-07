package httproxy

import (
	"flag"
	"fmt"
	"log"
	"testing"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/config/backendconfig"
	"github.com/fumin/httproxy/monero"
)

func TestXMR(t *testing.T) {
	b := newBackend(t)
	defer b.Close()

	// Transfer to server's address.
	amount := monero.ToPico(0.00001)
	transferReq := monero.TransferReq{Destinations: []monero.Destination{
		{
			Address: b.serverXMRAddr,
			Amount:  amount,
		},
	}}
	transferResp, err := b.xmr.Transfer(transferReq)
	if err != nil {
		t.Errorf("%+v", err)
	}
	if transferResp.Amount != amount {
		t.Errorf("%+v", transferResp)
	}
	txid := transferResp.TxHash

	// Generate blocks to confirm tx.
	if _, err := b.xmrNode.Generateblocks(monero.GenerateblocksReq{AmountOfBlocks: 1, WalletAddress: b.xmrAddr}); err != nil {
		t.Errorf("%+v", err)
	}

	// Refresh and get tx info.
	if _, err := b.s.XMR.Refresh(monero.RefreshReq{}); err != nil {
		t.Errorf("%+v", err)
	}
	getTxByIdResp, err := b.s.XMR.GetTransferByTxid(monero.GetTransferByTxidReq{Txid: txid})
	if err != nil {
		t.Errorf("%+v", err)
	}
	tx := getTxByIdResp.Transfers[0]
	if !(tx.Type == "in" && tx.Amount == amount) {
		t.Errorf("%+v", getTxByIdResp)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	m.Run()
}

type backend struct {
	s             *Server
	serverXMRAddr string

	xmrNode *monero.Node
	xmr     *monero.Wallet
	xmrAddr string
}

func newBackend(t *testing.T) *backend {
	backendCfg := backendconfig.Default

	// Setup server.
	b := &backend{}
	cfg := config.Config{
		Addr: ":0",
		Root: t.TempDir(),
		DB:   fmt.Sprintf("file:/%s?vfs=memdb", t.Name()),
		XMR:  backendCfg.TestXMR0,
	}
	var err error
	b.s, err = NewServer(cfg)
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	addrResp, err := b.s.XMR.GetAddress(monero.GetAddressReq{})
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	b.serverXMRAddr = addrResp.Address

	// Setup monero wallet.
	b.xmrNode, err = monero.NewNode(backendCfg.Monerod)
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	b.xmr = monero.NewWallet(backendCfg.TestXMR1)
	addrResp, err = b.xmr.GetAddress(monero.GetAddressReq{})
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	b.xmrAddr = addrResp.Address
	if err := fundXMRWallet(b.xmrNode, b.xmr); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}

	return b
}

func fundXMRWallet(node *monero.Node, w *monero.Wallet) error {
	// Make sure wallet has at least minUnlocked funds.
	minUnlocked := monero.ToPico(1)

	// Return early if wallet has sufficient balance.
	addrResp, err := w.GetAddress(monero.GetAddressReq{})
	if err != nil {
		return errors.Wrap(err, "")
	}
	addr := addrResp.Address
	if _, err := w.Refresh(monero.RefreshReq{}); err != nil {
		return errors.Wrap(err, "")
	}
	balance, err := w.GetBalance(monero.GetBalanceReq{})
	if err != nil {
		return errors.Wrap(err, "")
	}
	if balance.UnlockedBalance >= minUnlocked {
		return nil
	}

	// Generate blocks to wallet.
	if _, err := node.Generateblocks(monero.GenerateblocksReq{AmountOfBlocks: 60, WalletAddress: addr}); err != nil {
		return errors.Wrap(err, "")
	}
	if _, err := w.Refresh(monero.RefreshReq{}); err != nil {
		return errors.Wrap(err, "")
	}
	balance, err = w.GetBalance(monero.GetBalanceReq{})
	if err != nil {
		return errors.Wrap(err, "")
	}
	if balance.UnlockedBalance < minUnlocked {
		return errors.Errorf("balance %d < %d", balance.UnlockedBalance, minUnlocked)
	}
	return nil
}

func (b *backend) Close() {
	b.s.Close()
}
