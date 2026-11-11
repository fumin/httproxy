package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/config/backendconfig"
	"github.com/fumin/httproxy/util/monero"
)

func mainWithErr() error {
	signalCtx, unregisterSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer unregisterSignal()

	// Start commands.
	numExited := 0
	cmds, exits, err := startCmds()
	defer func() {
		for range len(cmds) - numExited {
			<-exits
		}
	}()
	if err != nil {
		return errors.Wrap(err, "")
	}
	if err := checkBackends(); err != nil {
		return errors.Wrap(err, "")
	}
	if err := createXMRWalletIfMissing(xmrWallet{name: "dev1"}); err != nil {
		return errors.Wrap(err, "")
	}
	log.Printf("backends started")

	// Stop all commands upon receiving Ctrl-C or if any command prematurely exited.
	select {
	case <-signalCtx.Done():
		unregisterSignal()
		stopAll(cmds)
	case ex := <-exits:
		numExited++
		stopAll(cmds)
		return errors.Errorf("%s exited prematurely: %v", strings.Join(ex.cmd.Args, " "), ex.err)
	}

	return nil
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	if err := mainWithErr(); err != nil {
		log.Fatalf("%+v", err)
	}
}

func checkBackends() error {
	start := time.Now()
	var err error
	for {
		err = checkBackendsOnce()
		if err == nil {
			return nil
		}
		if time.Now().Sub(start) > 30*time.Second {
			return err
		}
		<-time.After(time.Second)
	}
}

func checkBackendsOnce() error {
	for _, w := range xmrWallets() {
		if _, err := monero.NewWallet(w.host).GetBalance(monero.GetBalanceIn{}); err != nil {
			return errors.Wrap(err, w.host)
		}
	}
	return nil
}

const xmrWalletPassword = "regtest"

func createXMRWalletIfMissing(w xmrWallet) error {
	keyPath := w.file() + ".keys"
	_, err := os.Stat(keyPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return errors.Wrap(err, "")
	}

	create := exec.Command("monero-wallet-cli",
		"--daemon-address", backendconfig.Default.Monerod,
		"--generate-new-wallet", w.file(),
		"--password", xmrWalletPassword,
		"--mnemonic-language", "English",
		"--log-file", "/dev/null",
	)
	if err := create.Run(); err != nil {
		return errors.Wrap(err, strings.Join(create.Args, " "))
	}
	if _, err := os.Stat(keyPath); err != nil {
		return errors.Wrap(err, "monero-wallet-cli did not create the wallet")
	}

	return nil
}

type xmrWallet struct {
	name string
	host string
}

func (w xmrWallet) dir() string {
	return filepath.Join(config.DevRoot, "monero_wallet", w.name)
}

func (w xmrWallet) file() string {
	return filepath.Join(w.dir(), w.name)
}

func xmrWallets() []xmrWallet {
	cfg := backendconfig.Default
	return []xmrWallet{
		{name: "dev0", host: cfg.DevXMR0},
		{name: "test0", host: cfg.TestXMR0},
		{name: "test1", host: cfg.TestXMR1},
	}
}

type exitEvent struct {
	cmd *exec.Cmd
	err error
}

func startCmds() ([]*exec.Cmd, chan exitEvent, error) {
	exits := make(chan exitEvent)
	cmds := make([]*exec.Cmd, 0)
	start := func(args []string) error {
		cmdStr := strings.Join(args, " ")
		cmd := exec.Command(args[0], args[1:]...)
		if _, err := cmd.StdinPipe(); err != nil {
			return errors.Wrap(err, cmdStr)
		}
		if err := cmd.Start(); err != nil {
			return errors.Wrap(err, cmdStr)
		}
		cmds = append(cmds, cmd)
		go func() { exits <- exitEvent{cmd: cmd, err: cmd.Wait()} }()
		return nil
	}

	if err := start([]string{"monerod", "--regtest", "--offline", "--fixed-difficulty=1", "--keep-fakechain"}); err != nil {
		return cmds, exits, errors.Wrap(err, "")
	}
	if err := waitMonerod(); err != nil {
		return cmds, exits, errors.Wrap(err, "")
	}

	for _, w := range xmrWallets() {
		if err := os.MkdirAll(w.dir(), 0700); err != nil {
			return cmds, exits, errors.Wrap(err, "")
		}
		if err := createXMRWalletIfMissing(w); err != nil {
			return cmds, exits, errors.Wrap(err, w.name)
		}
		if err := start(moneroRPCCmd(w)); err != nil {
			return cmds, exits, errors.Wrap(err, "")
		}
	}

	return cmds, exits, nil
}

func waitMonerod() error {
	node, err := monero.NewNode(backendconfig.Default.Monerod)
	if err != nil {
		return errors.Wrap(err, "")
	}
	start := time.Now()
	for {
		_, err := node.GetFeeEstimate(monero.GetFeeEstimateIn{})
		if err == nil {
			return nil
		}
		if time.Since(start) > 30*time.Second {
			return errors.Wrap(err, "monerod did not start")
		}
		<-time.After(time.Second)
	}
}

func moneroRPCCmd(w xmrWallet) []string {
	logFile := filepath.Join(w.dir(), "rpc.log")

	splitted := strings.SplitN(w.host, ":", 2)
	ip, port := splitted[0], splitted[1]

	return []string{
		"monero-wallet-rpc",
		"--daemon-address", backendconfig.Default.Monerod,
		"--wallet-file", w.file(),
		"--password", xmrWalletPassword,
		"--rpc-bind-ip", ip,
		"--rpc-bind-port", port,
		"--log-file", logFile,
		"--disable-rpc-login",

		// Somehow version 0.18.5.4 needs this
		"--allow-mismatched-daemon-version",
	}
}

func stopAll(cmds []*exec.Cmd) {
	for _, c := range cmds {
		c.Process.Signal(os.Interrupt)
	}
	time.AfterFunc(10*time.Second, func() {
		for _, c := range cmds {
			c.Process.Kill()
		}
	})
}
