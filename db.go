package httproxy

import (
	"context"
	"database/sql"
	"time"
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
	CreateAt   time.Time `json:",omitzero"`
}
