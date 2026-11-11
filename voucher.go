package httproxy

import (
	"sync"
	"time"

	"github.com/fumin/httproxy/util"
	"github.com/pkg/errors"
)

const (
	voucherPrefix = "voucher_"
)

var errNoVoucher = errors.Errorf("no voucher")

type voucherDealer struct {
	sync.RWMutex
	vouchers map[string]Session
	kill     chan (struct{})
}

func newVoucherDealer() *voucherDealer {
	v := &voucherDealer{vouchers: make(map[string]Session), kill: make(chan struct{})}
	go func() {
		for {
			select {
			case <-v.kill:
				return
			case <-time.After(5 * time.Minute):
				v.deleteExpired()
			}
		}
	}()
	return v
}

func (v *voucherDealer) Close() {
	close(v.kill)
}

func (v *voucherDealer) deal() Session {
	id := voucherPrefix + util.RandStr()
	s := Session{ID: id, UsageLimit: 1e9, CreateAt: time.Now()}

	v.Lock()
	v.vouchers[s.ID] = s
	v.Unlock()

	return s
}

func (v *voucherDealer) use(id string, usage int64) {
	v.Lock()
	defer v.Unlock()

	s, ok := v.vouchers[id]
	if !ok {
		return
	}
	s.Usage += usage
	v.vouchers[s.ID] = s
}

func (v *voucherDealer) get(id string) (Session, error) {
	v.RLock()
	defer v.RUnlock()
	s, ok := v.vouchers[id]
	if !ok {
		return Session{}, errNoVoucher
	}
	return s, nil
}

func (v *voucherDealer) all() []Session {
	v.RLock()
	sessions := make([]Session, 0, len(v.vouchers))
	for _, s := range v.vouchers {
		sessions = append(sessions, s)
	}
	v.RUnlock()
	return sessions
}

func (v *voucherDealer) deleteExpired() {
	now := time.Now()
	v.Lock()
	for id, s := range v.vouchers {
		old := s.CreateAt.AddDate(0, 0, 7).Before(now)
		depleted := s.Usage > s.UsageLimit
		if old || depleted {
			delete(v.vouchers, id)
		}
	}
	v.Unlock()
}
