package httproxy

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"time"

	"github.com/pkg/errors"
)

type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Session struct {
	ID         string
	UsageLimit time.Duration
	Usage      time.Duration
	CreateAt   time.Time
}

func (s *Session) MarshalJSONTo(enc *jsontext.Encoder) error {
	sj := struct {
		ID         string
		UsageLimit int
		Usage      int
		CreateAt   time.Time `json:",omitzero"`
	}{
		ID:         s.ID,
		UsageLimit: int(s.UsageLimit.Seconds()),
		Usage:      int(s.Usage.Seconds()),
		CreateAt:   s.CreateAt,
	}
	return json.MarshalEncode(enc, sj)
}

// ByID loads the session whose id is s.ID.
func (s *Session) ByID(ctx context.Context, db DB) error {
	if err := db.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM session WHERE id=?", s.ID).Scan(&s.ID, &s.UsageLimit, &s.Usage); err != nil {
		return errors.Wrap(err, "")
	}
	s.UsageLimit *= time.Second
	s.Usage *= time.Second
	return nil
}
