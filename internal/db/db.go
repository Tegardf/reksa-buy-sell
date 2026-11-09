package db

import (
	"context"
	"fmt"

	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a Postgres connection pool from a connection URL.
//
// Every new connection gets the shopspring/decimal type registered, so
// NUMERIC columns read and write as decimal.Decimal instead of float.
// NewPool pings the database before returning so a bad URL or an
// unreachable server fails at startup, not on the first request. The
// caller must Close the pool when done.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		pgxdecimal.Register(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create db pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// WithTx runs fn inside a database transaction.
//
// If fn returns nil, the transaction is committed. If fn returns an error
// (or panics), the transaction is rolled back and the error is returned.
// The deferred Rollback is a no-op once Commit has succeeded.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx)} ()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
