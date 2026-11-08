package order

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Side is the direction of an order: buying or selling fund units.
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// Status is the lifecycle state of an order. New orders start as PENDING.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusConfirmed Status = "CONFIRMED"
	StatusSettled   Status = "SETTLED"
	StatusRejected  Status = "REJECTED"
)

// Errors returned by the service. The HTTP handler checks them with
// errors.Is to choose the response status code.
var (
	ErrValidation = errors.New("validation failed")
	ErrNotFound   = errors.New("order not found")
	ErrKeyReuse   = errors.New("idempotency key reused with a different request")
)

// minAmount is the smallest order amount allowed.
var minAmount = decimal.NewFromInt(10_000)

// Order is a stored buy or sell order.
type Order struct {
	ID        uuid.UUID       `json:"id"`
	UserID    uuid.UUID       `json:"user_id"`
	FundID    string          `json:"fund_id"`
	Side      Side            `json:"side"`
	Amount    decimal.Decimal `json:"amount"`
	Status    Status          `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
}

// CreateOrderRequest is the JSON body of POST /v1/orders.
type CreateOrderRequest struct {
	FundID string          `json:"fund_id"`
	Side   Side            `json:"side"`
	Amount decimal.Decimal `json:"amount"`
}

// Validate checks the request against the business rules:
//   - fund_id must not be empty
//   - side must be BUY or SELL
//   - amount must be at least minAmount (10,000)
//   - amount may have at most 4 decimal places (matches NUMERIC(20,4))
//
// It returns an error wrapping ErrValidation on the first rule that fails,
// or nil if the request is valid.
func (r CreateOrderRequest) Validate() error {
	if r.FundID == "" {
		return fmt.Errorf("%w: fund_id is required", ErrValidation)
	}
	if r.Side != SideBuy && r.Side != SideSell {
		return fmt.Errorf("%w: side must be BUY or SELL", ErrValidation)
	}
	if r.Amount.LessThan(minAmount) {
		return fmt.Errorf("%w: amount must be at least %s", ErrValidation, minAmount)
	}
	if !r.Amount.Equal(r.Amount.Round(4)) {
		return fmt.Errorf("%w: amount allows at most 4 decimal places", ErrValidation)
	}
	return nil
}
