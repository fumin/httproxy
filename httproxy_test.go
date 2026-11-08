package httproxy

import (
	"flag"
	"fmt"
	"log"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/client"
	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/config/backendconfig"
	"github.com/fumin/httproxy/util/monero"
)

func TestTopup(t *testing.T) {
	b := newBackend(t)
	defer b.Close()

	// Send money.
	usageLimit := 5 * time.Minute
	amount := monero.Piconero(usageLimit.Seconds() * client.ProxyPriceXMRPiconeroPerSecond)
	txid := transfer(b.s.XMR, b.xmr, amount, b.xmrNode)

	// Top up.
	topupIn := client.TopupIn{Method: client.MethodXMR, PaymentTx: txid}
	topupOut, err := b.c.Topup(topupIn)
	if err != nil {
		t.Fatalf("%+v", err)
	}

	// Check usage limit.
	usageOut, err := b.c.Usage(client.UsageIn{SessionID: topupOut.ID})
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if !(usageOut.UsageLimit == usageLimit) {
		t.Errorf("%+v", usageOut)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	m.Run()
}

type backend struct {
	s *Server

	xmrNode *monero.Node
	xmr     *monero.Wallet
	xmrAddr string

	hs *httptest.Server
	c  *client.Client
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

	// Setup monero wallet.
	b.xmrNode, err = monero.NewNode(backendCfg.Monerod)
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	b.xmr = monero.NewWallet(backendCfg.TestXMR1)
	addrResp, err := b.xmr.GetAddress(monero.GetAddressIn{AddressIndex: []int{0}})
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	b.xmrAddr = addrResp.Address
	if err := fundXMRWallet(b.xmrNode, b.xmr); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}

	// Start server listening on random port.
	b.hs = httptest.NewServer(b.s.Server.Handler)
	b.c = client.NewClient(b.hs.Listener.Addr().String())
	return b
}

func (b *backend) Close() {
	b.hs.Close()
	b.s.Close()
}

func fundXMRWallet(node *monero.Node, w *monero.Wallet) error {
	// Make sure wallet has at least minUnlocked funds.
	minUnlocked := monero.Picos(1)

	// Return early if wallet has sufficient balance.
	addrResp, err := w.GetAddress(monero.GetAddressIn{AddressIndex: []int{0}})
	if err != nil {
		return errors.Wrap(err, "")
	}
	addr := addrResp.Address
	if _, err := w.Refresh(monero.RefreshIn{}); err != nil {
		return errors.Wrap(err, "")
	}
	balance, err := w.GetBalance(monero.GetBalanceIn{})
	if err != nil {
		return errors.Wrap(err, "")
	}
	if balance.UnlockedBalance >= minUnlocked {
		return nil
	}

	// Generate blocks to wallet.
	if _, err := node.Generateblocks(monero.GenerateblocksIn{AmountOfBlocks: 60, WalletAddress: addr}); err != nil {
		return errors.Wrap(err, "")
	}
	if _, err := w.Refresh(monero.RefreshIn{}); err != nil {
		return errors.Wrap(err, "")
	}
	balance, err = w.GetBalance(monero.GetBalanceIn{})
	if err != nil {
		return errors.Wrap(err, "")
	}
	if balance.UnlockedBalance < minUnlocked {
		return errors.Errorf("balance %d < %d", balance.UnlockedBalance, minUnlocked)
	}
	return nil
}

func transfer(dst, src *monero.Wallet, amount monero.Piconero, node *monero.Node) string {
	// Get addresses.
	srcAddrOut, err := src.GetAddress(monero.GetAddressIn{})
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	dstAddrOut, err := dst.GetAddress(monero.GetAddressIn{})
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}

	// Do the transfer.
	transferIn := monero.TransferIn{Destinations: []monero.Destination{
		{
			Address: dstAddrOut.Address,
			Amount:  amount,
		},
	}}
	transferOut, err := src.Transfer(transferIn)
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}

	// Refresh wallet.
	if _, err := node.Generateblocks(monero.GenerateblocksIn{AmountOfBlocks: 1, WalletAddress: srcAddrOut.Address}); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	if _, err := dst.Refresh(monero.RefreshIn{}); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}

	return transferOut.TxHash
}
