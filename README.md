# reksa

![CI](https://github.com/Tegardf/reksa/actions/workflows/ci.yml/badge.svg)

An order service for mutual fund (*reksa dana*) transactions, written in Go and backed by PostgreSQL.

It accepts **BUY** and **SELL** orders through an HTTP API. It is built so that a request can be retried without creating a second order:

- **Idempotency keys.** Each create request carries an `Idempotency-Key`. A retry with the same key and body gets the stored response back. A retry with the same key and a different body is rejected.
- **Transactional outbox.** The order, its `order.created` event and the idempotency record are written in a single database transaction. The order and its event are always saved together or not at all.
- **Exact money handling.** Amounts use `shopspring/decimal` and are stored as `NUMERIC(20,4)`, never as floats.

## Tech stack

| Area          | Choice                                                       |
| ------------- | ------------------------------------------------------------ |
| Language      | Go (see `go.mod` for the version)                            |
| HTTP          | Standard library `net/http` (Go 1.22+ method/path routing)   |
| Database      | PostgreSQL 16 via `pgx/v5` connection pool                   |
| Migrations    | [`golang-migrate`](https://github.com/golang-migrate/migrate) |
| Logging       | `log/slog` (JSON to stdout)                                  |
| Tests         | `testify` + `testcontainers-go` (real Postgres in Docker)    |
| CI            | GitHub Actions: `golangci-lint`, build, race-enabled tests   |

## Project layout

```
.
├── cmd/order-service/main.go   # Entry point: config, DB pool, routes, graceful shutdown
├── internal/
│   ├── config/config.go        # Reads settings from environment variables
│   ├── db/db.go                # pgx pool setup + WithTx transaction helper
│   └── order/
│       ├── model.go            # Order types, statuses, validation, errors
│       ├── handler.go          # HTTP handlers and error-to-status mapping
│       ├── service.go          # Business logic: idempotency + outbox in one tx
│       ├── repository.go       # SQL queries
│       └── service_test.go     # Integration tests against a real Postgres
├── migrations/                 # Database schema (golang-migrate format)
├── docker-compose.yml          # Local Postgres
├── Makefile                    # Shortcuts for run / test / migrate
└── .github/workflows/ci.yml    # Lint + build + test on push and PR
```

## Prerequisites

- **Go**, matching the version in `go.mod`
- **Docker**, for the local database and for the integration tests
- **GNU Make**. On Windows, use Git Bash, WSL, or install it with `choco install make` / `scoop install make`.
- **golang-migrate CLI**, needed by `make migrate-up` / `make migrate-down`:
  ```bash
  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
  ```

## Configuration

The service reads its settings from environment variables. The Makefile loads a `.env` file in the project root (if there is one) and exports its values to every command.

| Variable       | Required | Default | Description                                          |
| -------------- | -------- | ------- | ---------------------------------------------------- |
| `DATABASE_URL` | yes      | none    | Postgres connection string                           |
| `HTTP_ADDR`    | no       | `:8000` | Address the HTTP server listens on, e.g. `:8080`     |

Example `.env` for the bundled docker-compose database:

```env
DATABASE_URL=postgres://reksa:reksa@localhost:5432/reksa?sslmode=disable
HTTP_ADDR=:8000
```

> `.env` is git-ignored. Do not commit real credentials.

## Getting started

```bash
# 1. Start Postgres
docker compose up -d

# 2. Create your .env (see Configuration above)

# 3. Apply the database schema
make migrate-up

# 4. Run the service
make run
```

Check that it is up:

```bash
curl http://localhost:8000/healthz   # -> ok
```

## Make commands

| Command             | Runs                                                       | What it does |
| ------------------- | ---------------------------------------------------------- | ------------ |
| `make run`          | `go run ./cmd/order-service`                               | Builds and starts the HTTP server in the foreground. Stop it with `Ctrl+C`; the server then shuts down gracefully, finishing in-flight requests for up to 10 seconds. Needs `DATABASE_URL` and a reachable database, or it exits at startup. |
| `make test`         | `go test -race ./...`                                      | Runs all tests with the race detector. The order tests start a throwaway `postgres:16-alpine` container through testcontainers, apply `migrations/`, and run against it. **Docker must be running.** It does not use your `.env` database. |
| `make migrate-up`   | `migrate -path migrations -database "$DATABASE_URL" up`    | Applies every pending migration to the database in `DATABASE_URL`. Run it once after creating a fresh database and again whenever a new migration is added. |
| `make migrate-down` | `migrate -path migrations -database "$DATABASE_URL" down 1`| Rolls back **only the most recent** migration. With the current single migration, this **drops all tables and their data** (`orders`, `idempotency_keys`, `outbox`). |

## API

All `/v1` endpoints need an `X-User-ID` header containing a UUID. This header is a placeholder for authentication and will be replaced by JWT. Orders are scoped to the user: a user can only read their own orders.

### `POST /v1/orders`: create an order

**Headers**

| Header            | Required | Notes                                    |
| ----------------- | -------- | ---------------------------------------- |
| `X-User-ID`       | yes      | UUID of the caller                       |
| `Idempotency-Key` | yes      | Any unique string, 1–255 characters      |
| `Content-Type`    | yes      | `application/json`                       |

**Body**

```json
{
  "fund_id": "RDPU-01",
  "side": "BUY",
  "amount": "1000000"
}
```

Validation rules:

- `fund_id` must not be empty
- `side` must be `BUY` or `SELL`
- `amount` must be at least **10,000** and have no more than **4 decimal places**
- Unknown fields are rejected, and the body may be at most 1 MB

**Example**

```bash
curl -i -X POST http://localhost:8000/v1/orders \
  -H "X-User-ID: 3f1c2b7e-8a4d-4e7a-9b1a-2c6d5e4f7a10" \
  -H "Idempotency-Key: order-001" \
  -H "Content-Type: application/json" \
  -d '{"fund_id":"RDPU-01","side":"BUY","amount":"1000000"}'
```

**Response `201 Created`**

```json
{
  "id": "b6a1f0f2-...",
  "user_id": "3f1c2b7e-8a4d-4e7a-9b1a-2c6d5e4f7a10",
  "fund_id": "RDPU-01",
  "side": "BUY",
  "amount": "1000000",
  "status": "PENDING",
  "created_at": "2026-11-05T10:00:00Z"
}
```

**Idempotency behaviour**

| Situation                                   | Result |
| ------------------------------------------- | ------ |
| New key                                     | Order created, `201` |
| Same key, same body (retry)                 | The original response is returned unchanged, plus the header `Idempotent-Replayed: true`. No new order is created. |
| Same key, different body                    | `422 Unprocessable Entity` |
| Same key, different user                    | Treated as a separate key, because keys are scoped per user |

Equivalent amounts count as the same body, so `"1000000"` and `"1000000.00"` match.

### `GET /v1/orders/{id}`: get an order

```bash
curl http://localhost:8000/v1/orders/<order-id> \
  -H "X-User-ID: 3f1c2b7e-8a4d-4e7a-9b1a-2c6d5e4f7a10"
```

Returns `200` with the order, or `404` if it does not exist or belongs to a different user.

### `GET /healthz`: health check

Pings the database. Returns `200 ok`, or `503 db unavailable` if the database cannot be reached within 2 seconds.

### Error format

Errors are returned as JSON:

```json
{ "error": "validation failed: side must be BUY or SELL" }
```

| Status | When |
| ------ | ---- |
| `400`  | Invalid JSON, missing or too-long `Idempotency-Key`, invalid order id, failed validation |
| `401`  | Missing or invalid `X-User-ID` |
| `404`  | Order not found |
| `422`  | Idempotency key reused with a different body |
| `500`  | Unexpected error (the details are logged, not returned) |

## How creating an order works

Everything below runs inside **one database transaction** (`service.CreateOrder`):

1. Validate the request and compute a SHA-256 hash of its body.
2. **Claim the key** with `INSERT INTO idempotency_keys ... ON CONFLICT DO NOTHING`.
   - If the key was already taken, read the stored row. If the hash matches, replay the stored response. If it does not, return `ErrKeyReuse`.
3. Insert the order with status `PENDING`.
4. Insert an `order.created` event into the `outbox` table, topic `orders`.
5. Save the response code and body on the idempotency row so retries can replay it.

When concurrent requests use the same key, Postgres makes the second `INSERT` wait on the primary key until the first transaction commits. So exactly one request creates the order and the others replay its response. `TestCreateOrder_ConcurrentSameKey` checks this with 50 parallel requests.

> The outbox is only written for now. No relay that publishes unsent rows (`sent_at IS NULL`) to a message broker exists yet.

## Database schema

| Table              | Purpose |
| ------------------ | ------- |
| `orders`           | The orders. `amount NUMERIC(20,4)`; `side` is restricted to `BUY`/`SELL` by a check constraint. |
| `idempotency_keys` | One row per `(user_id, key)` holding the request hash and the saved response. |
| `outbox`           | Events waiting to be published; `sent_at` is set once an event has been sent. |

Order statuses defined in code: `PENDING`, `CONFIRMED`, `SETTLED`, `REJECTED`. Only `PENDING` is used so far.

## CI

`.github/workflows/ci.yml` runs on every push to `main` and on every pull request:

- **lint**: `golangci-lint`
- **test**: `go build ./...` then `go test -race -count=1 ./...`
