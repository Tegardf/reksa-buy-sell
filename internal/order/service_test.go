package order_test

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Tegardf/reksa/internal/db"
	"github.com/Tegardf/reksa/internal/order"
)

var (
	pool *pgxpool.Pool
	svc  *order.Service
)

// TestMain starts one Postgres container for the whole package.
// It applies the migrations, creates the shared pool and service used by
// every test, runs the tests, then closes the pool and removes the
// container. Docker must be running.
func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("reksa"),
		postgres.WithUsername("reksa"),
		postgres.WithPassword("reksa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("dsn: %v", err)
	}

	mig, err := migrate.New("file://../../migrations", dsn)
	if err != nil {
		log.Fatalf("migrate init: %v", err)
	}
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migrate up: %v", err)
	}

	pool, err = db.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	svc = order.NewService(pool)

	code := m.Run()
	pool.Close()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

// newRequest builds a valid BUY request for fund RDPU-01 with the given
// amount. It panics if amount is not a valid decimal string.
func newRequest(amount string) order.CreateOrderRequest {
	return order.CreateOrderRequest{
		FundID: "RDPU-01",
		Side:   order.SideBuy,
		Amount: decimal.RequireFromString(amount),
	}
}

// countOrders returns how many orders exist in the database for userID.
func countOrders(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE user_id = $1`, userID).Scan(&n)
	require.NoError(t, err)
	return n
}

// countEvents returns how many outbox events exist for orders owned by
// userID, matching on the user_id inside the JSON payload.
func countEvents(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE payload->'order'->>'user_id' = $1`,
		userID.String()).Scan(&n)
	require.NoError(t, err)
	return n
}

// TestCreateOrder_Success checks that a new key creates one order and one
// outbox event and returns 201 (not a replay).
func TestCreateOrder_Success(t *testing.T) {
	user := uuid.New()
	res, err := svc.CreateOrder(context.Background(), user, "k1", newRequest("1000000"))

	require.NoError(t, err)
	require.Equal(t, 201, res.Code)
	require.False(t, res.Replayed)
	require.Equal(t, 1, countOrders(t, user))
	require.Equal(t, 1, countEvents(t, user))
}

// TestCreateOrder_RetryReplaysSameResponse checks that retrying with the
// same key and an equal body returns the original response unchanged and
// does not create a second order.
func TestCreateOrder_RetryReplaysSameResponse(t *testing.T) {
	ctx, user := context.Background(), uuid.New()
	first, err := svc.CreateOrder(ctx, user, "k1", newRequest("1000000"))
	require.NoError(t, err)

	// "1000000.00" is the same amount, so it must count as the same request.
	second, err := svc.CreateOrder(ctx, user, "k1", newRequest("1000000.00"))
	require.NoError(t, err)

	require.True(t, second.Replayed)
	require.JSONEq(t, string(first.Body), string(second.Body))
	require.Equal(t, 1, countOrders(t, user))
}

// TestCreateOrder_SameKeyDifferentBody checks that reusing a key with a
// different body fails with ErrKeyReuse and creates no extra order.
func TestCreateOrder_SameKeyDifferentBody(t *testing.T) {
	ctx, user := context.Background(), uuid.New()
	_, err := svc.CreateOrder(ctx, user, "k1", newRequest("1000000"))
	require.NoError(t, err)

	_, err = svc.CreateOrder(ctx, user, "k1", newRequest("2000000"))
	require.ErrorIs(t, err, order.ErrKeyReuse)
	require.Equal(t, 1, countOrders(t, user))
}

// TestCreateOrder_KeysAreScopedPerUser checks that two users can use the
// same key string independently; each gets their own order.
func TestCreateOrder_KeysAreScopedPerUser(t *testing.T) {
	ctx, alice, bob := context.Background(), uuid.New(), uuid.New()
	_, err := svc.CreateOrder(ctx, alice, "shared", newRequest("1000000"))
	require.NoError(t, err)
	_, err = svc.CreateOrder(ctx, bob, "shared", newRequest("1000000"))
	require.NoError(t, err)

	require.Equal(t, 1, countOrders(t, alice))
	require.Equal(t, 1, countOrders(t, bob))
}

// TestCreateOrder_ConcurrentSameKey sends 50 identical requests at the
// same time with one key. Exactly one must create the order; the other 49
// must replay its response, and only one order and one event may exist.
func TestCreateOrder_ConcurrentSameKey(t *testing.T) {
	const n = 50
	ctx, user := context.Background(), uuid.New()
	results := make([]order.Result, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.CreateOrder(ctx, user, "same-key", newRequest("1000000"))
		}()
	}
	wg.Wait()

	created := 0
	for i := range n {
		require.NoError(t, errs[i])
		require.JSONEq(t, string(results[0].Body), string(results[i].Body))
		if !results[i].Replayed {
			created++
		}
	}
	require.Equal(t, 1, created, "exactly one request should create the order")
	require.Equal(t, 1, countOrders(t, user))
	require.Equal(t, 1, countEvents(t, user))
}
