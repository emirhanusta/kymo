.PHONY: run build tidy lint test migrate docker-up docker-down web web-dev

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
