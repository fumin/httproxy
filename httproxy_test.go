package httproxy

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/api"
	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/config/backendconfig"
	"github.com/fumin/httproxy/util/monero"
)

func TestTopup(t *testing.T) {
	b := newBackend(t)
	t.Cleanup(b.Close)

	// Send money.
	usageLimit := 5 * time.Minute
	amount := monero.Piconero(usageLimit.Seconds() * api.ProxyPriceXMRPiconeroPerSecond)
	txid := transfer(b.s.XMR, b.xmr, amount, b.xmrNode)

	// Top up.
	topupIn := api.TopupIn{Method: api.MethodXMR, PaymentTx: txid}
	topupOut, err := b.c.Topup(topupIn)
	if err != nil {
		t.Fatalf("%+v", err)
	}

	// Check usage limit.
	usageOut, err := b.c.Usage(api.UsageIn{SessionID: topupOut.ID})
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if !(usageOut.UsageLimit == usageLimit && usageOut.Usage == 0) {
		t.Errorf("%+v", usageOut)
	}

	// Use proxy and check usage.
	if err := useProxy(b.hs.Listener.Addr().String(), topupOut.ID); err != nil {
		t.Fatalf("%+v", err)
	}
	usageOut, err = b.c.Usage(api.UsageIn{SessionID: topupOut.ID})
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if usageOut.Usage <= 0 {
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
	c  *api.Client
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
		Secret: config.Secret{
			Password: "x",
		},
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
	b.c = api.NewClient(b.hs.Listener.Addr().String())
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

func useProxy(proxyAddr, sessionID string) error {
	// Start target server.
	const body = "hello"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer target.Close()
	targetHost := target.Listener.Addr().String()

	// Connect to proxy.
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return errors.Wrap(err, "")
	}
	br := bufio.NewReader(conn)

	// Do a CONNECT request.
	proxyAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(sessionID+":"))
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\nHost: %s\nProxy-Authorization: %s\n\n", targetHost, targetHost, proxyAuth)
	if _, err := io.WriteString(conn, connectReq); err != nil {
		return errors.Wrap(err, "")
	}
	connectResp, err := http.ReadResponse(br, nil)
	if err != nil {
		return errors.Wrap(err, "")
	}
	if connectResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(connectResp.Body)
		return errors.Errorf("%d %s", connectResp.StatusCode, b)
	}

	// Do a request to target through the proxy.
	req, err := http.NewRequest(http.MethodGet, "http://"+targetHost+"/", nil)
	if err != nil {
		return errors.Wrap(err, "")
	}
	req.Close = true
	if err := req.Write(conn); err != nil {
		return errors.Wrap(err, "")
	}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "")
	}
	if string(b) != body {
		return errors.Errorf("%d %s", resp.StatusCode, b)
	}

	// Wait till the proxy http.Handler has returned with all defer functions finished running.
	// This is achieved by waiting for the connection to be closed, since this only happens when a http.Handler has returned.
	if _, err := io.Copy(io.Discard, br); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}
