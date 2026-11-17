.PHONY: run build tidy lint test test-db migrate docker-up docker-down web web-dev

BINARY := bin/kymo
CMD    := ./cmd/kymo

# Override on the command line: make migrate DSN="postgres://..."
DSN ?= postgres://kymo:kymo@localhost:5432/kymo?sslmode=disable

# ---------------------------------------------------------------------------
# Development
# ---------------------------------------------------------------------------

## run: run the service locally (requires TimescaleDB to be reachable).
run:
	go run $(CMD)

## build: compile the kymo binary to $(BINARY).
build:
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o $(BINARY) $(CMD)

## web: build the frontend into frontend/dist (serve it with server.static_dir).
web:
	cd frontend && npm ci && npx ng build

## web-dev: run the Angular dev server on :4200 (proxies /api to :8080).
web-dev:
	cd frontend && npm start

## tidy: synchronise go.mod/go.sum and remove unused dependencies.
tidy:
	go mod tidy

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

## lint: run golangci-lint with the project's configuration.
lint:
	golangci-lint run ./...

## test: run all tests with the race detector enabled.
test:
	go test -race -count=1 -timeout=60s ./...

## test-db: run the repository integration tests against a throwaway TimescaleDB container.
TEST_DB_NAME  ?= kymo-test-db
TEST_DB_PORT  ?= 55433
TEST_DB_IMAGE ?= timescale/timescaledb:latest-pg16
test-db:
	@set -e; \
	trap 'docker stop $(TEST_DB_NAME) >/dev/null 2>&1 || true' EXIT INT TERM; \
	docker stop $(TEST_DB_NAME) >/dev/null 2>&1 || true; \
	docker run -d --rm --name $(TEST_DB_NAME) -e POSTGRES_USER=kymo -e POSTGRES_PASSWORD=kymo \
	  -p 127.0.0.1:$(TEST_DB_PORT):5432 $(TEST_DB_IMAGE) >/dev/null; \
	echo "waiting for $(TEST_DB_NAME)..."; \
	ok=; for i in $$(seq 1 90); do \
	  if docker exec $(TEST_DB_NAME) pg_isready -U kymo -h 127.0.0.1 >/dev/null 2>&1 \
	     && docker exec $(TEST_DB_NAME) psql -U kymo -h 127.0.0.1 -tAc 'select 1' >/dev/null 2>&1; then ok=1; break; fi; \
	  sleep 1; done; \
	[ -n "$$ok" ] || { echo "database did not become ready"; exit 1; }; \
	KYMO_TEST_DSN='postgres://kymo:kymo@localhost:$(TEST_DB_PORT)/postgres?sslmode=disable' \
	  go test -race -count=1 ./internal/repository/...

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

## migrate: apply all SQL migrations by hand (the service also applies them on startup).
migrate:
	@for f in migrations/*.sql; do echo "applying $$f"; psql "$(DSN)" -v ON_ERROR_STOP=1 -f "$$f" || exit 1; done

# ---------------------------------------------------------------------------
# Docker Compose
# ---------------------------------------------------------------------------

## docker-up: build and start TimescaleDB + the recorder.
docker-up:
	docker compose up -d --build

## docker-down: stop all services and remove volumes.
docker-down:
	docker compose down -v

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

## help: print this help message.
help:
	@echo "Available targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/  /'
