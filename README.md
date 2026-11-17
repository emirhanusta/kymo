# KYMO

A recorder for container lifecycles. KYMO watches Docker daemons and Kubernetes
clusters, records every start, crash, OOM kill and resource sample in
TimescaleDB, and turns that history into a stability score per container —
like a kymograph tracing a needle over chart paper.

![Overview](docs/screenshots/01-overview.jpg)

## What it does

- **Lifecycle history** — start / stop / crash / OOM events per container,
  with exit codes and reasons, kept across restarts of KYMO itself.
- **Resource traces** — CPU, memory and network rates over 1h / 6h / 24h / 7d
  (long ranges are downsampled server-side).
- **Stability score (0–100)** — penalties for restarts, crashes, OOM kills,
  CPU volatility and reporting gaps over a configurable window.
- **Multiple sources** — any number of Docker daemons and Kubernetes clusters;
  an unreachable source is retried in the background and shown as such.
- **Management** — start / stop / restart / kill Docker containers, delete
  pods, scale and restart Deployments, stream logs.
- **Kubernetes extras** — workload detection (Deployment, Job, CronJob, …) and
  CronJob execution history.
- **Archive / ignore** — hide containers without losing their history, or stop
  tracking them entirely.

| | |
|---|---|
| ![Containers](docs/screenshots/02-containers.jpg) | ![Container detail](docs/screenshots/03-detail.jpg) |
| ![Resource traces](docs/screenshots/04-resources.jpg) | ![Command palette](docs/screenshots/05-command-palette.jpg) |

## Quick start (Docker Compose)

```bash
docker compose up -d --build
```

Open <http://localhost:8080>. With no config file mounted, KYMO watches the
local Docker daemon. The service runs as a non-root user, so it needs the
Docker socket's group:

```bash
DOCKER_GID=$(stat -c '%g' /var/run/docker.sock) docker compose up -d --build
```

## Running locally

Requirements: Go 1.24+, Node 20+, a TimescaleDB instance.

```bash
cp config.example.yaml config.yaml   # adjust sources / DSN
make web                             # build the UI into frontend/dist
make run                             # API + collectors (set server.static_dir to serve the UI)
```

For frontend development run `make web-dev` (Angular dev server on :4200,
proxying `/api` to :8080). Database migrations are applied automatically on
startup; `make migrate DSN=...` applies them by hand.

## Configuration

See [`config.example.yaml`](config.example.yaml). Every key can be overridden
with an environment variable prefixed `KYMO_`, e.g. `KYMO_DATABASE_DSN`,
`KYMO_COLLECTOR_SCORING_WINDOW`.

Kubernetes sources need a kubeconfig; CPU/memory sampling requires
metrics-server in the cluster.

> **Security note:** the API has no authentication and exposes management
> actions (kill, delete pod, scale). Run it on a trusted network or behind an
> authenticating proxy, and be careful which kubeconfig context a source uses.

## Keyboard

| Key | Action |
|---|---|
| `⌘K` / `Ctrl+K` | Command palette — jump to any page or container |
| `1`–`6` | Switch tabs |
| `/` | Focus the search box |
| `Esc` | Close dialogs |

## Project layout

```
cmd/kymo            entry point
internal/collector  Docker and Kubernetes collectors
internal/service    recorder (event pipeline, scoring loop) and management actions
internal/repository TimescaleDB persistence + embedded migration runner
internal/handler    REST API (/api/v1) and SPA serving
migrations          SQL schema (embedded into the binary)
frontend            Angular web UI
```

## Development

```bash
make test    # go test -race ./...
make lint    # golangci-lint
```
