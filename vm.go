package httproxy

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/fumin/httproxy/api"
	"github.com/fumin/httproxy/auth"
	"github.com/fumin/httproxy/util"
	"github.com/fumin/httproxy/util/gvisor"
	"github.com/fumin/httproxy/util/monero"
	"github.com/fumin/httproxy/util/pseudoterminal"
)

const (
	// From the client: terminal input.
	v5Stdin = 0
	// From the server: terminal output.
	// The terminal combines stdout and stderr, so the stderr channel 2 is unused.
	v5Stdout = 1
	// From the server: a JSON Status, sent once after the shell exits.
	v5Error = 3
	// From the client: a JSON terminal size, like {"Width":80,"Height":24}.
	v5Resize = 4
	// A message whose payload is the channel that the sender has closed.
	v5Close = 255
)

//go:embed tmpl/vmdoc/vmdoc.html
var vmDocHTML string
var vmDocTmpl = template.Must(commonTmpl().Parse(vmDocHTML))

//go:embed tmpl/vmdoc/vmdoc.md
var vmDocMD []byte
var vmDocBody = mustRenderMarkdown(vmDocMD)

func VMDoc(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
		Common      pageCommon
		PathVMSetID string
		Body        template.HTML
	}{
		Common:      getPageCommon(s, r),
		PathVMSetID: PathVMSetID,
		Body:        vmDocBody,
	}
	vmDocTmpl.Execute(w, page)
}

func VMTopup(ctx context.Context, s *Server, in api.VMTopupIn) (out VM, err error) {
	received, err := xmrReceived(s.XMR, s.XMRAddr, in.PaymentTx)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	usageLimit := received * 3600 / api.VMPriceXMRPicoPerHour

	newVM := false
	id := in.ID
	if id == "" {
		newVM = true
		id = util.RandStr()
	}

	if newVM {
		const minTopup = time.Minute
		minTopupXMR := monero.Piconero(math.Ceil(minTopup.Hours() * api.VMPriceXMRPicoPerHour))
		if received < minTopupXMR {
			return out, errors.Errorf("topup amount %d less than minimum %d (%s)", received, minTopupXMR, minTopup)
		}
	}

	if newVM {
		if !s.vmService.HasSpareCapacity() {
			return out, errors.Errorf("out of capacity, your transaction is not being used, and can be reused again later")
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "INSERT INTO payment (method, tx, service, serviceID) VALUES (?, ?, ?, ?)", in.Method, in.PaymentTx, serviceVM, id); err != nil {
		return out, errors.Wrap(err, "")
	}
	if newVM {
		if _, err := tx.ExecContext(ctx, "INSERT INTO vm (id, usageLimit, usage, createAt) VALUES (?, ?, ?, ?)", id, usageLimit, 0, time.Now().Unix()); err != nil {
			return out, errors.Wrap(err, "")
		}
	} else {
		res, err := tx.ExecContext(ctx, "UPDATE vm SET usageLimit=usageLimit+? WHERE id=? AND usageLimit > usage", usageLimit, id)
		if err != nil {
			return out, errors.Wrap(err, "")
		}
		nrow, err := res.RowsAffected()
		if err != nil {
			return out, errors.Wrap(err, "")
		}
		if nrow != 1 {
			return out, errors.Errorf("vm %s has ended", id)
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM vm WHERE id=?", id).Scan(&out.ID, &out.UsageLimit, &out.Usage); err != nil {
		return out, errors.Wrap(err, "")
	}

	if err := tx.Commit(); err != nil {
		return out, errors.Wrap(err, "")
	}

	if newVM {
		started, err := s.vmService.Put(out.ID)
		if err != nil {
			return out, nil
		}
		select {
		case <-ctx.Done():
		case <-started:
		}
	}
	out.Running = (s.vmService.Get(out.ID) != nil)

	return
}

func VMInfo(ctx context.Context, s *Server, in api.VMInfoIn) (out VM, err error) {
	if err := s.DB.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM vm WHERE id=?", in.ID).Scan(&out.ID, &out.UsageLimit, &out.Usage); err != nil {
		return out, errors.Wrap(err, "")
	}
	out.Running = (s.vmService.Get(out.ID) != nil)
	return
}

func VMSetID(s *Server, w http.ResponseWriter, r *http.Request) {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host {
		http.Error(w, "", http.StatusForbidden)
		return
	}
	vmID := r.FormValue("vmid")

	c := &http.Cookie{
		Name:     cookieNameVMID,
		Value:    vmID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().AddDate(3, 0, 0),
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, PathVMLoginPage, http.StatusSeeOther)
}

//go:embed tmpl/vmloginpage.html
var vmLoginPageHTML string
var vmLoginPageTmpl = template.Must(template.New("").Parse(vmLoginPageHTML))

func VMLoginPage(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
		PathVMLogin string
	}{
		PathVMLogin: PathVMLogin,
	}
	vmLoginPageTmpl.Execute(w, page)
}

func VMLogin(s *Server, w http.ResponseWriter, r *http.Request) {
	id := auth.GetToken(r, cookieNameVMID)
	if id == "" {
		http.Error(w, "no id", http.StatusBadRequest)
		return
	}

	box := s.vmService.Get(id)
	if box == nil {
		var m VM
		if err := s.DB.QueryRowContext(r.Context(), "SELECT id, usageLimit, usage FROM vm WHERE id=?", id).Scan(&m.ID, &m.UsageLimit, &m.Usage); err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, fmt.Sprintf("%q not found", id), http.StatusBadRequest)
			} else {
				http.Error(w, fmt.Sprintf("%+v", err), http.StatusInternalServerError)
			}
			return
		}
		http.Error(w, fmt.Sprintf("out of capacity %+v", m), http.StatusInternalServerError)
		return
	}

	const (
		v5Protocol = "v5.channel.k8s.io"
		v4Protocol = "v4.channel.k8s.io"
	)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{v5Protocol, v4Protocol}})
	if err != nil {
		// Accept has already written the error response.
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() == "" {
		conn.Close(websocket.StatusPolicyViolation, "subprotocol must be "+v5Protocol+" or "+v4Protocol)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := vmLogin(ctx, conn, box); err != nil {
		conn.Close(websocket.StatusInternalError, "")
		return
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

func vmLogin(parentCtx context.Context, conn *websocket.Conn, box *gvisor.Sandbox) error {
	ptm, pts, err := pseudoterminal.Open()
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer ptm.Close()
	defer pts.Close()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	readC := make(chan error, 1)
	go func() {
		// readV5 has to use parentCtx because it calls conn.Read, which
		// closes conn immediately when its context is canceled.
		// Closing conn immediately would make the outer
		// conn.Close(websocket.Status...) fail.
		readC <- readV5(parentCtx, conn, ptm)
	}()
	writeC := make(chan error, 1)
	go func() {
		var err error
		defer func() { writeC <- err }()

		buf := make([]byte, 32<<10)
		buf[0] = v5Stdout
		var n int
		for {
			n, err = ptm.Read(buf[1:])
			if n > 0 {
				if err = conn.Write(ctx, websocket.MessageBinary, buf[:1+n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	execC := make(chan error, 1)
	go func() {
		vmLoginCmd := []string{"/bin/bash", "--login"}
		execC <- box.Exec(ctx, vmLoginCmd, pts, pts, pts, &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0})
	}()

	var execErr error
	select {
	case err := <-readC:
		return errors.Wrap(err, "")
	case err := <-writeC:
		return errors.Wrap(err, "")
	case execErr = <-execC:
	}

	// Send the shell's remaining output to the client.
	pts.Close()
	ptm.SetReadDeadline(time.Now().Add(time.Second))
	select {
	case <-time.After(5 * time.Second):
		cancel()
		<-writeC
	case <-writeC:
	}

	if ctx.Err() != nil {
		return nil
	}
	status, err := json.Marshal(v5Status(execErr))
	if err != nil {
		return errors.Wrap(err, "")
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer writeCancel()
	if err := conn.Write(writeCtx, websocket.MessageBinary, append([]byte{v5Error}, status...)); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

// readV5 reads messages from conn, until it fails or ctx is done.
func readV5(ctx context.Context, conn *websocket.Conn, ptm *os.File) error {
	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			return nil
		}
		if typ != websocket.MessageBinary || len(msg) == 0 {
			return errors.Errorf("invalid message %d %q", typ, msg)
		}
		switch ch, data := msg[0], msg[1:]; ch {
		case v5Stdin:
			if _, err := ptm.Write(data); err != nil {
				return errors.Wrap(err, "")
			}
		case v5Resize:
			var size struct{ Width, Height uint16 }
			if err := json.Unmarshal(data, &size); err != nil {
				return errors.Wrapf(err, "%q", data)
			}
			if err := pseudoterminal.SetWinsize(ptm, size.Width, size.Height); err != nil {
				return errors.Wrap(err, "")
			}
		case v5Close:
			if len(data) == 1 && data[0] == v5Stdin {
				// Signal end of input to the shell, as typing Ctrl-D does.
				if _, err := ptm.Write([]byte{4}); err != nil {
					return errors.Wrap(err, "")
				}
			}
		default:
			return errors.Errorf("unknown channel %d", ch)
		}
	}
}

// v5Status returns the Kubernetes Status object reporting the result of a command.
func v5Status(execErr error) any {
	type cause struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	type details struct {
		Causes []cause `json:"causes"`
	}
	type status struct {
		Metadata struct{} `json:"metadata"`
		Status   string   `json:"status"`
		Message  string   `json:"message,omitempty"`
		Reason   string   `json:"reason,omitempty"`
		Details  *details `json:"details,omitempty"`
	}

	if execErr == nil {
		return status{Status: "Success"}
	}
	var exitErr *exec.ExitError
	if errors.As(execErr, &exitErr) && exitErr.ExitCode() > 0 {
		code := strconv.Itoa(exitErr.ExitCode())
		return status{
			Status:  "Failure",
			Message: fmt.Sprintf("command terminated with non-zero exit code: %s", code),
			Reason:  "NonZeroExitCode",
			Details: &details{Causes: []cause{{Reason: "ExitCode", Message: code}}},
		}
	}
	return status{Status: "Failure", Message: execErr.Error()}
}

type vmService struct {
	sync.RWMutex
	boxes map[string]*gvisor.Sandbox

	maxNum      int
	cfg         gvisor.Config
	granularity time.Duration

	db     *sql.DB
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newVMService(ctx context.Context, db *sql.DB, maxNum int, cfg gvisor.Config, granularity time.Duration) (*vmService, error) {
	runnables, err := runnableVMs(ctx, db)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	if len(runnables) > maxNum {
		runnables = runnables[:maxNum]
	}

	serviceCtx, serviceCancel := context.WithCancel(context.Background())
	v := &vmService{
		boxes:       make(map[string]*gvisor.Sandbox),
		maxNum:      maxNum,
		cfg:         cfg,
		granularity: granularity,
		db:          db,
		ctx:         serviceCtx,
		cancel:      serviceCancel,
	}
	for _, m := range runnables {
		started, err := v.Put(m.ID)
		if err != nil {
			v.Close()
			return nil, errors.Wrap(err, "")
		}
		select {
		case <-ctx.Done():
			v.Close()
			return nil, errors.Wrap(ctx.Err(), "")
		case <-started:
		}
	}

	return v, nil
}

// Close stops all sandboxes, and waits for their final usage to be recorded.
func (v *vmService) Close() error {
	// Ensure no future v.wg.Add calls are possible before we call wg.Wait.
	// This is achieved by setting v.ctx.Err within a mutex.Lock, since
	// wg.Add is called only in Put, which also checks v.ctx.Err.
	v.Lock()
	v.cancel()
	v.Unlock()

	v.wg.Wait()
	return nil
}

func (v *vmService) HasSpareCapacity() bool {
	v.RLock()
	l := len(v.boxes)
	v.RUnlock()
	return l < v.maxNum
}

func (v *vmService) Put(id string) (<-chan struct{}, error) {
	v.Lock()
	defer v.Unlock()

	if v.ctx.Err() != nil {
		return nil, errors.Wrap(v.ctx.Err(), "")
	}
	if len(v.boxes) >= v.maxNum {
		return nil, errors.Errorf("no capacity")
	}

	// Insert a placeholder so that len(v.boxes) reflects true the number of
	// currently running sandboxes.
	v.boxes[id] = nil
	started := make(chan struct{})
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		v.runSandbox(id, v.cfg, started)
	}()

	return started, nil
}

func (v *vmService) Get(id string) *gvisor.Sandbox {
	v.RLock()
	box := v.boxes[id]
	v.RUnlock()
	return box
}

func (v *vmService) runSandbox(id string, cfg gvisor.Config, started chan<- struct{}) {
	defer func() {
		v.Lock()
		if box := v.boxes[id]; box != nil {
			box.Close()
		}
		delete(v.boxes, id)
		v.Unlock()
	}()

	for {
		var box *gvisor.Sandbox
		var err error
		retryMust(v.ctx, func() error {
			box, err = gvisor.NewSandbox(v.ctx, cfg)
			return err
		})
		if v.ctx.Err() != nil {
			return
		}
		v.Lock()
		v.boxes[id] = box
		v.Unlock()
		if started != nil { // notify call on the first start.
			close(started)
			started = nil
		}

	UsageLoop:
		for {
			var remaining time.Duration
			retryMust(v.ctx, func() error {
				remaining, err = remainingTime(v.ctx, v.db, id)
				return err
			})
			if v.ctx.Err() != nil {
				return
			}
			if remaining < 0 {
				return
			}

			sleepT := min(remaining+time.Second, v.granularity)
			startT := time.Now()
			select {
			case <-v.ctx.Done():
				updateUsage(v.db, id, startT)
				return
			case <-box.Done():
				updateUsage(v.db, id, startT)
				break UsageLoop
			case <-time.After(sleepT):
				updateUsage(v.db, id, startT)
			}
		}
	}
}

func updateUsage(db *sql.DB, id string, startT time.Time) error {
	usage := int(math.Ceil(time.Now().Sub(startT).Seconds()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "UPDATE vm SET usage=usage+? WHERE id=?", usage, id); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func remainingTime(ctx context.Context, db *sql.DB, id string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var m VM
	if err := db.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM vm WHERE id=?", id).Scan(&m.ID, &m.UsageLimit, &m.Usage); err != nil {
		return 0, errors.Wrap(err, "")
	}
	remaining := time.Duration(m.UsageLimit-m.Usage) * time.Second
	return remaining, nil
}

func runnableVMs(ctx context.Context, db *sql.DB) ([]VM, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, usageLimit, usage, createAt FROM vm WHERE usageLimit > usage")
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	defer rows.Close()

	vms := make([]VM, 0)
	for rows.Next() {
		var m VM
		var createAt int64
		if err := rows.Scan(&m.ID, &m.UsageLimit, &m.Usage, &createAt); err != nil {
			return nil, errors.Wrap(err, "")
		}
		m.CreateAt = time.Unix(createAt, 0)
		vms = append(vms, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "")
	}
	return vms, nil
}

func retryMust(ctx context.Context, f func() error) error {
	backoff := wait.Backoff{
		Duration: time.Second,
		Factor:   2,
		Jitter:   1,
		Steps:    math.MaxInt,
		Cap:      time.Minute,
	}
	condition := func(ctx context.Context) (bool, error) {
		return f() == nil, nil
	}
	return wait.ExponentialBackoffWithContext(ctx, backoff, condition)
}
