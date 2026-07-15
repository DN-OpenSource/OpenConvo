package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tx aliases pgx.Tx so callers outside the store package don't import pgx.
type Tx = pgx.Tx

// Querier abstracts *pgxpool.Pool and pgx.Tx so every query can run either
// standalone or inside a transaction (required for the outbox pattern:
// mutation + event row commit atomically).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
