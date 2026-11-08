CREATE TABLE orders (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL,
    fund_id    TEXT NOT NULL,
    side       TEXT NOT NULL CHECK (side IN ('BUY', 'SELL')),
    amount     NUMERIC(20, 4) NOT NULL CHECK (amount > 0),
    status     TEXT NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_user_id ON orders (user_id);

CREATE TABLE idempotency_keys (
    user_id       UUID NOT NULL,
    key           TEXT NOT NULL,
    request_hash  TEXT NOT NULL,
    response_code INT,
    response_body JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

CREATE TABLE outbox (
    id          BIGSERIAL PRIMARY KEY,
    topic       TEXT NOT NULL,
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at     TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unsent ON outbox (id) WHERE sent_at IS NULL;