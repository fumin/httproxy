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
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/config/backendconfig"
	"github.com/fumin/httproxy/util/monero"
)

func mainWithErr() error {
	signalCtx, unregisterSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer unregisterSignal()

	// Pin the current thread, in case Pdeathsig gets prematurely sent.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Start commands.
	ctx, cancel := context.WithCancel(signalCtx)
	numExited := 0
	cmds, exits, err := startCmds(ctx)
	defer func() {
		cancel()
		for range len(cmds) - numExited {
			<-exits
		}
	}()
	if err != nil {
		return err
	}

	// Wait for backends to start.
	if err := checkBackends(ctx); err != nil {
		return err
	}
	if err := createXMRWalletIfMissing(ctx, xmrWallet{name: "dev1"}); err != nil {
		return err
	}
	log.Printf("backends started")

	// Return upon signaled, or if any command prematurely exited.
	select {
	case <-signalCtx.Done():
		// Let a second signal kill us immediately.
		unregisterSignal()
	case ex := <-exits:
		numExited++

		// Ctrl-C broadcasts signals in no particular order, so our
		// children may receive it earlier than us.
		if signalCtx.Err() != nil {
			return nil
		}
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

func checkBackends(ctx context.Context) error {
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

		// Sleep.
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "")
		case <-time.After(time.Second):
		}
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

func createXMRWalletIfMissing(ctx context.Context, w xmrWallet) error {
	keyPath := w.file() + ".keys"
	_, err := os.Stat(keyPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return errors.Wrap(err, "")
	}

	create := exec.CommandContext(ctx, "monero-wallet-cli",
		"--daemon-address", backendconfig.Default.Monerod,
		"--generate-new-wallet", w.file(),
		"--password", xmrWalletPassword,
		"--mnemonic-language", "English",
		"--log-file", "/dev/null",
	)
	create.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	// Interrupt instead of the default SIGKILL when ctx is done, so the wallet is not left half written.
	create.Cancel = func() error { return create.Process.Signal(os.Interrupt) }
	create.WaitDelay = 10 * time.Second
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

func startCmds(ctx context.Context) ([]*exec.Cmd, chan exitEvent, error) {
	exits := make(chan exitEvent)
	cmds := make([]*exec.Cmd, 0)
	start := func(args ...string) error {
		cmdStr := strings.Join(args, " ")
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		// Interrupt instead of the default SIGKILL, so that commands shutdown gracefully, such as wallets saving their state.
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		// SIGKILL if a command has not exited in term after canceled.
		cmd.WaitDelay = 10 * time.Second
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

	// Start monerod.
	if err := start("monerod", "--regtest", "--offline", "--fixed-difficulty=1", "--keep-fakechain"); err != nil {
		return cmds, exits, errors.Wrap(err, "")
	}
	if err := waitMonerod(ctx); err != nil {
		return cmds, exits, errors.Wrap(err, "")
	}

	// Start wallet RPCs.
	for _, w := range xmrWallets() {
		if err := os.MkdirAll(w.dir(), 0700); err != nil {
			return cmds, exits, errors.Wrap(err, "")
		}
		if err := createXMRWalletIfMissing(ctx, w); err != nil {
			return cmds, exits, errors.Wrap(err, w.name)
		}
		// SIGKILL instead of SIGTERM, since upon SIGTERM, monero-wallet-rpc sometimes hangs.
		if err := start(moneroRPCCmd(w)...); err != nil {
			return cmds, exits, errors.Wrap(err, "")
		}
	}

	return cmds, exits, nil
}

func waitMonerod(ctx context.Context) error {
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

		// Sleep.
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "")
		case <-time.After(time.Second):
		}
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
