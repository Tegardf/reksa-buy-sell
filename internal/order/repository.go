package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the minimal database interface the repository needs. Both
// *pgxpool.Pool and pgx.Tx satisfy it, so the same query methods work
// inside or outside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Repository holds the SQL queries for orders, idempotency keys and the
// outbox. It has no state; the connection or transaction is passed to
// each method as q.
type Repository struct{}

// storedKey is a row from idempotency_keys: the request fingerprint and
// the response that was saved for it.
type storedKey struct {
	RequestHash  string
	ResponseCode int
	ResponseBody json.RawMessage
}

// ClaimKey tries to reserve the idempotency key for this user.
//
// It inserts a row with ON CONFLICT DO NOTHING and returns true if the row
// was inserted (this request is the first to use the key), or false if
// the key already existed. When two transactions race on the same key,
// Postgres makes the second one wait until the first commits, so only one
// of them gets true.
func (Repository) ClaimKey(ctx context.Context, q DBTX, userID uuid.UUID, key, hash string) (bool, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO idempotency_keys (user_id, key, request_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO NOTHING`, userID, key, hash)
	if err != nil {
		return false, fmt.Errorf("claim key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetKey loads the stored request hash and saved response for an existing
// idempotency key. It is used on retries to replay the original response.
func (Repository) GetKey(ctx context.Context, q DBTX, userID uuid.UUID, key string) (storedKey, error) {
	var k storedKey
	err := q.QueryRow(ctx, `
		SELECT request_hash, response_code, response_body
		FROM idempotency_keys WHERE user_id = $1 AND key = $2`,
		userID, key).Scan(&k.RequestHash, &k.ResponseCode, &k.ResponseBody)
	if err != nil {
		return k, fmt.Errorf("get key: %w", err)
	}
	return k, nil
}

// SaveResponse stores the HTTP status code and body on the idempotency key
// row, so later retries with the same key get exactly the same response.
func (Repository) SaveResponse(ctx context.Context, q DBTX, userID uuid.UUID, key string, code int, body json.RawMessage) error {
	_, err := q.Exec(ctx, `
		UPDATE idempotency_keys SET response_code = $3, response_body = $4
		WHERE user_id = $1 AND key = $2`, userID, key, code, body)
	if err != nil {
		return fmt.Errorf("save response: %w", err)
	}
	return nil
}

// InsertOrder saves a new order. The database sets created_at, and
// InsertOrder writes that value back into o.CreatedAt.
func (Repository) InsertOrder(ctx context.Context, q DBTX, o *Order) error {
	err := q.QueryRow(ctx, `
		INSERT INTO orders (id, user_id, fund_id, side, amount, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		o.ID, o.UserID, o.FundID, o.Side, o.Amount, o.Status).Scan(&o.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

// InsertOutbox adds an event to the outbox table under the given topic.
// Rows stay there with sent_at = NULL until a relay publishes them.
func (Repository) InsertOutbox(ctx context.Context, q DBTX, topic string, payload json.RawMessage) error {
	_, err := q.Exec(ctx, `INSERT INTO outbox (topic, payload) VALUES ($1, $2)`, topic, payload)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

// GetOrder loads one order by ID, but only if it belongs to userID.
// It returns ErrNotFound if there is no such order for that user.
func (Repository) GetOrder(ctx context.Context, q DBTX, userID, id uuid.UUID) (Order, error) {
	var o Order
	err := q.QueryRow(ctx, `
		SELECT id, user_id, fund_id, side, amount, status, created_at
		FROM orders WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&o.ID, &o.UserID, &o.FundID, &o.Side, &o.Amount, &o.Status, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, fmt.Errorf("get order: %w", err)
	}
	return o, nil
}
