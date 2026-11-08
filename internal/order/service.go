package order

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tegardf/reksa/internal/db"
)

// Service holds the business logic for orders.
type Service struct {
	pool *pgxpool.Pool
	repo Repository
}

// NewService creates a Service that uses pool for all database access.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Result is the response to send, stored so retries get the same one.
type Result struct {
	Code     int             // HTTP status code
	Body     json.RawMessage // JSON response body
	Replayed bool            // true if this is a stored response from an earlier request
}

// orderCreatedEvent is the payload written to the outbox when an order
// is created.
type orderCreatedEvent struct {
	EventID    uuid.UUID `json:"event_id"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	Order      Order     `json:"order"`
}

// CreateOrder creates an order exactly once per (userID, key).
//
// It validates the request and fingerprints the body, then, in a single
// transaction:
//  1. Claims the idempotency key.
//  2. If the key was already used: returns the stored response when the
//     body matches (Replayed = true), or ErrKeyReuse when it does not.
//  3. Otherwise: inserts the order as PENDING, writes an "order.created"
//     event to the outbox, and saves the 201 response on the key.
//
// Because all of this runs in one transaction, the order, its event and
// the saved response are committed together or not at all.
func (s *Service) CreateOrder(ctx context.Context, userID uuid.UUID, key string, req CreateOrderRequest) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	hash, err := hashRequest(req)
	if err != nil {
		return Result{}, err
	}

	var res Result
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		claimed, err := s.repo.ClaimKey(ctx, tx, userID, key, hash)
		if err != nil {
			return err
		}

		// Retry: replay the stored response, or refuse a different body.
		if !claimed {
			stored, err := s.repo.GetKey(ctx, tx, userID, key)
			if err != nil {
				return err
			}
			if stored.RequestHash != hash {
				return ErrKeyReuse
			}
			res = Result{Code: stored.ResponseCode, Body: stored.ResponseBody, Replayed: true}
			return nil
		}

		// First time: create the order and its event.
		o := Order{
			ID:     uuid.New(),
			UserID: userID,
			FundID: req.FundID,
			Side:   req.Side,
			Amount: req.Amount,
			Status: StatusPending,
		}
		if err := s.repo.InsertOrder(ctx, tx, &o); err != nil {
			return err
		}

		event, err := json.Marshal(orderCreatedEvent{
			EventID:    uuid.New(),
			Type:       "order.created",
			OccurredAt: o.CreatedAt,
			Order:      o,
		})
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		if err := s.repo.InsertOutbox(ctx, tx, "orders", event); err != nil {
			return err
		}

		body, err := json.Marshal(o)
		if err != nil {
			return fmt.Errorf("marshal order: %w", err)
		}
		res = Result{Code: http.StatusCreated, Body: body}
		return s.repo.SaveResponse(ctx, tx, userID, key, res.Code, body)
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// GetOrder returns the order with the given ID if it belongs to userID,
// or ErrNotFound otherwise.
func (s *Service) GetOrder(ctx context.Context, userID, id uuid.UUID) (Order, error) {
	return s.repo.GetOrder(ctx, s.pool, userID, id)
}

// hashRequest returns a SHA-256 fingerprint (hex) of the request body.
// It is used to detect whether a retry sends the same body as the
// original request. decimal normalizes "1000.00" and "1000" to the same
// string, so equal amounts hash the same.
func hashRequest(req CreateOrderRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("hash request: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
