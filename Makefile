# Load variables from .env (if present) and export them to every command.
-include .env
export

.PHONY: migrate-up migrate-down run test

# Apply all pending database migrations to DATABASE_URL.
migrate-up:
	migrate -path migrations -database "${DATABASE_URL}" up

# Roll back only the most recent migration.
migrate-down:
	migrate -path migrations -database "${DATABASE_URL}" down 1

# Start the HTTP server.
run:
	go run ./cmd/order-service

# Run all tests with the race detector (requires Docker).
test:
	go test -race ./...
