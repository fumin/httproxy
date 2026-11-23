package httproxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/errors"

	"github.com/fumin/httproxy/api"
	"github.com/fumin/httproxy/util"
	"github.com/fumin/httproxy/util/monero"
)

func TestVMTopup(t *testing.T) {
	t.Parallel()
	b := newBackend(t, insertVM)
	t.Cleanup(b.Close)

	// Send money.
	const usageLimit time.Duration = time.Hour
	amount := monero.Piconero(usageLimit.Hours() * api.VMPriceXMRPicoPerHour)
	txid0 := transfer(b.s.XMR, b.xmr, amount, b.xmrNode)

	// Top up.
	topupIn0 := api.VMTopupIn{Method: api.MethodXMR, PaymentTx: txid0}
	topupOut0, err := b.c.VMTopup(topupIn0)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if !topupOut0.Running {
		t.Errorf("%+v", topupOut0)
	}
	id := topupOut0.ID
	vm0, err := b.c.VMInfo(api.VMInfoIn{ID: id})
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if !(vm0.UsageLimit == int(usageLimit.Seconds()) && vm0.Usage == 0 && vm0.Running) {
		t.Errorf("%+v", vm0)
	}

	// Use VM.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Sleep a while to record usage.
	if _, _, err := vmRun(ctx, b.hs.URL, id, "sleep 0.2\nexit\n"); err != nil {
		t.Fatalf("%+v", err)
	}
	stdout, status, err := vmRun(ctx, b.hs.URL, id, "echo hello\nexit\n")
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if status["status"] != "Success" {
		t.Fatalf("%s %q", mustJSON(t, status), stdout)
	}
	if !hasLine(stdout, "hello") {
		t.Fatalf("%q", stdout)
	}

	// Top up for the same VM.
	txid1 := transfer(b.s.XMR, b.xmr, amount, b.xmrNode)
	topupIn1 := api.VMTopupIn{Method: api.MethodXMR, PaymentTx: txid1, ID: id}
	if _, err := b.c.VMTopup(topupIn1); err != nil {
		t.Fatalf("%+v", err)
	}
	vm1, err := b.c.VMInfo(api.VMInfoIn{ID: id})
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if !(vm1.UsageLimit == int(2*usageLimit.Seconds()) && vm1.Usage > vm0.Usage) {
		t.Errorf("%+v", vm1)
	}
}

func TestVMLogin(t *testing.T) {
	t.Parallel()
	b := newBackend(t, insertVM)
	t.Cleanup(b.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vms, err := runnableVMs(ctx, b.s.DB)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	conn, _, err := websocket.Dial(ctx, b.hs.URL+"/VMLogin", vmDialOptions(vms[0].ID))
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer conn.CloseNow()
	send := func(ch byte, data string) {
		if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{ch}, data...)); err != nil {
			t.Fatalf("%+v", err)
		}
	}

	var stdout bytes.Buffer
	var status map[string]any
	// read reads a message, appending stdout, and parsing the status.
	read := func() {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("%+v %q", err, stdout.String())
		}
		switch msg[0] {
		case v5Stdout:
			stdout.Write(msg[1:])
		case v5Error:
			if err := json.Unmarshal(msg[1:], &status); err != nil {
				t.Fatalf("%+v", err)
			}
		default:
			t.Fatalf("unexpected channel %d %q", msg[0], msg)
		}
	}
	readUntil := func(want string) {
		for !strings.Contains(stdout.String(), want) {
			if status != nil {
				t.Fatalf("%q does not contain %q", stdout.String(), want)
			}
			read()
		}
	}

	send(v5Resize, `{"Width":123,"Height":45}`)
	send(v5Stdin, "echo user=$(id -un) size=$(stty size) pwd=$PWD\n")
	readUntil("user=ubuntu size=45 123 pwd=/home/ubuntu")

	// Resizing while a command runs sends it SIGWINCH.
	send(v5Stdin, `sh -c 'trap "echo winch=\$(stty size); exit" WINCH; echo ready; while :; do sleep 0.1; done'`+"\n")
	readUntil("ready\r\n")
	send(v5Resize, `{"Width":67,"Height":8}`)
	readUntil("winch=8 67")

	// Ctrl-C interrupts the running command, but not the session.
	send(v5Stdin, `sh -c 'trap "echo interrupted; exit" INT; echo started; while :; do sleep 0.1; done'`+"\n")
	readUntil("started\r\n")
	send(v5Stdin, "\x03")
	readUntil("interrupted\r\n")

	send(v5Stdin, "exit 3\n")
	for status == nil {
		read()
	}
	if status["reason"] != "NonZeroExitCode" || !strings.Contains(string(mustJSON(t, status)), `"message":"3"`) {
		t.Fatalf("%s", mustJSON(t, status))
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("%+v", err)
	}
}

func insertVM(ctx context.Context, db *sql.DB) error {
	usageLimit := time.Hour
	if _, err := db.ExecContext(ctx, "INSERT INTO vm (id, usageLimit, usage, createAt) VALUES (?, ?, ?, ?)", util.RandStr(), int(usageLimit.Seconds()), 0, time.Now().Unix()); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	return b
}

// vmRun logs in to the VM id, types input into its shell, and returns the shell's output and exit status.
func vmRun(ctx context.Context, baseURL, id, input string) (string, map[string]any, error) {
	// Log in with the cookie, as a browser does.
	// The server only accepts the cookie from the same origin.
	opts := &websocket.DialOptions{
		Subprotocols: []string{"v5.channel.k8s.io"},
		HTTPHeader: http.Header{
			"Origin": []string{baseURL},
			"Cookie": []string{(&http.Cookie{Name: cookieNameVMID, Value: id}).String()},
		},
	}
	conn, _, err := websocket.Dial(ctx, baseURL+"/VMLogin", opts)
	if err != nil {
		return "", nil, errors.Wrap(err, "")
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{v5Stdin}, input...)); err != nil {
		return "", nil, errors.Wrap(err, "")
	}

	var stdout bytes.Buffer
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return stdout.String(), nil, errors.Wrapf(err, "%q", stdout.String())
		}
		switch msg[0] {
		case v5Stdout:
			stdout.Write(msg[1:])
		case v5Error:
			var status map[string]any
			if err := json.Unmarshal(msg[1:], &status); err != nil {
				return stdout.String(), nil, errors.Wrap(err, "")
			}
			return stdout.String(), status, nil
		default:
			return stdout.String(), nil, errors.Errorf("unexpected channel %d %q", msg[0], msg)
		}
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// hasLine reports whether the terminal output contains want as a whole line.
// This excludes the terminal echoing the command that printed want, such as "$ echo want".
func hasLine(output, want string) bool {
	output = ansiEscape.ReplaceAllString(output, "")
	for _, line := range strings.FieldsFunc(output, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if line == want {
			return true
		}
	}
	return false
}

// vmDialOptions returns the options for dialing /VMLogin, which takes the VM ID as a Bearer token.
func vmDialOptions(id string) *websocket.DialOptions {
	return &websocket.DialOptions{
		Subprotocols: []string{"v5.channel.k8s.io"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + id}},
	}
}
