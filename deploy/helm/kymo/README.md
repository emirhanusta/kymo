# KYMO Helm chart

Runs [KYMO](../../../README.md) inside the Kubernetes cluster it monitors
("in-cluster mode"): the pod uses its ServiceAccount, no kubeconfig is needed.
The chart installs KYMO, read-only RBAC, and (by default) a single-node
TimescaleDB.

```bash
helm install kymo deploy/helm/kymo -n kymo --create-namespace
kubectl -n kymo port-forward svc/kymo 8080:8080   # http://localhost:8080
helm test kymo -n kymo
```

> KYMO has **no authentication**. Keep it on `ClusterIP`/port-forward or put an
> authenticating proxy in front of the ingress.

## Prerequisites

- Kubernetes 1.23+, Helm 3.8+
- A default StorageClass (for the bundled database volume)
- metrics-server (optional, for CPU/memory samples)
- The image `ghcr.io/emirhanusta/kymo:<appVersion>` (published by the release
  workflow on `v*` tags). For a local build see "Local testing" below.

## What you must set

Nothing, for an evaluation install. Everything required is generated:

| Value | Default behaviour |
| --- | --- |
| `secretKey.value` / `secretKey.existingSecret` | generated once (48 chars) and preserved across upgrades via `lookup`. **Back it up**: it decrypts stored kubeconfigs and notification URLs |
| `timescaledb.auth.password` | generated once and preserved |
| `database.dsn` or `database.existingSecret` | **required** only with `timescaledb.enabled=false` |
| `ingress.hosts` | **required** with `ingress.enabled=true` |
| `server.publicUrl` | recommended when notifications are used (links in messages) |

Note: `lookup` returns nothing under `helm template` / `--dry-run`, so rendered
output shows throw-away random values; real installs/upgrades keep them stable.
`helm uninstall` removes the generated Secret but Kubernetes keeps the database
PVC: when reinstalling, delete the PVC (`data-<release>-kymo-timescaledb-0`) or
pass `timescaledb.auth.password` and `secretKey.value` explicitly.

## Install examples

### Bundled database (default)

```bash
helm install kymo deploy/helm/kymo -n kymo --create-namespace \
  --set timescaledb.persistence.size=20Gi \
  --set timescaledb.persistence.storageClass=fast-ssd
```

### External database

The database needs the TimescaleDB extension. Migrations run at startup.

```bash
helm install kymo deploy/helm/kymo -n kymo --create-namespace \
  --set timescaledb.enabled=false \
  --set database.dsn='postgres://kymo:PASS@db.example.com:5432/kymo?sslmode=require'
```

Prefer a Secret over a DSN in your shell history / values:

```bash
kubectl -n kymo create secret generic kymo-db --from-literal=dsn='postgres://...'
helm install kymo deploy/helm/kymo -n kymo \
  --set timescaledb.enabled=false \
  --set database.existingSecret=kymo-db      # key defaults to "dsn"
```

### Ingress

```yaml
# values-ingress.yaml
server:
  publicUrl: https://kymo.example.com
ingress:
  enabled: true
  className: nginx
  annotations:
    # log streaming uses long-lived SSE connections
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    # plus whatever authenticates users, e.g. oauth2-proxy auth-url annotations
  hosts:
    - host: kymo.example.com
  tls:
    - secretName: kymo-tls
      hosts: [kymo.example.com]
```

`helm install kymo deploy/helm/kymo -n kymo -f values-ingress.yaml`

### Notification channels

Channels in `notifications.channels` are written verbatim into `config.yaml`
(snake_case keys, see [`config.example.yaml`](../../../config.example.yaml)),
are locked in the UI and hot-reloaded. Webhook URLs would be plain text in a
ConfigMap, so store the config in a Secret:

```yaml
# values-notify.yaml
configInSecret: true
server:
  publicUrl: https://kymo.example.com
notifications:
  channels:
    - name: ops-slack
      type: slack
      url: https://hooks.slack.com/services/T000/B000/XXXX
      min_severity: critical
      include_logs: true
    - name: billing-webhook
      type: webhook
      url: https://example.com/kymo-hook
      secret: hmac-secret
```

Alternatively create channels in the UI: their URLs are then stored encrypted
in the database using `KYMO_SECRET_KEY`.

### Allow management actions

Default RBAC is strictly read-only and the in-cluster source is marked
`read_only`. To enable "restart pod", scale and rollout restart in the UI:

```bash
helm upgrade kymo deploy/helm/kymo -n kymo --reuse-values --set rbac.management.enabled=true
```

(this also flips the source to writable unless `cluster.readOnly` is set).

## Configuration reload

`config.yaml` is mounted as a directory at `/etc/kymo`. Changes to
`collector.sources` and notification channels (e.g. `helm upgrade` changing
`extraSources`/`notifications.channels`) are picked up by KYMO without a
restart (the kubelet refresh takes up to a minute or so, and KYMO watches the
ConfigMap's `..data` symlink swap). Other settings (intervals, log level, ...)
require a pod restart: `kubectl -n kymo rollout restart deploy/kymo`.
Changes to the generated/referenced Secrets restart the pod through the
`checksum/secret` annotation.

## RBAC

A ClusterRole + ClusterRoleBinding (`rbac.create`) grants cluster-wide:

| API group | Resources | Verbs | Why |
| --- | --- | --- | --- |
| core | pods | get, list, watch | pod informer: container lifecycle |
| core | pods/log | get | crash-log snapshots, live log viewer |
| core | events | get, list, watch | Kubernetes Events informer |
| core | namespaces | get, list, watch | UI namespace picker, connection test |
| apps | replicasets, deployments | get, list, watch | resolve ReplicaSet -> Deployment owner, deployment picker |
| batch | jobs | get, list, watch | resolve Job -> CronJob owner |
| metrics.k8s.io | pods | get, list | CPU/memory samples (optional) |
| core | pods | delete | only with `rbac.management.enabled` |
| apps | deployments | update, patch | only with `rbac.management.enabled` |

Using `rbac.create=false` requires you to bind an equivalent role to the
ServiceAccount yourself.

KYMO monitoring its own namespace needs no special handling.

## Local testing (minikube)

```bash
minikube image build -t ghcr.io/emirhanusta/kymo:0.1.0 .    # from the repo root
helm install kymo deploy/helm/kymo -n kymo --create-namespace \
  --set image.pullPolicy=Never
```

## Values

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `ghcr.io/emirhanusta/kymo` | image |
| `image.tag` | `""` (appVersion) | image tag |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `nameOverride`, `fullnameOverride` | `""` | |
| `replicaCount` | `1` | keep 1 (single writer); strategy is `Recreate` |
| `containerPort` | `8080` | |
| `resources` | 100m/128Mi req, 512Mi limit | |
| `podSecurityContext`, `securityContext` | non-root uid 10001, read-only rootfs, drop ALL, seccomp RuntimeDefault | |
| `podAnnotations`, `podLabels`, `priorityClassName` | | |
| `nodeSelector`, `tolerations`, `affinity` | | |
| `extraEnv`, `extraVolumes`, `extraVolumeMounts` | `[]` | |
| `startupProbe`, `livenessProbe`, `readinessProbe` | | HTTP `GET /api/v1/healthz` |
| `waitForDatabase.enabled` | `true` | init container waiting with `pg_isready` |
| `waitForDatabase.image.*`, `.resources` | timescaledb image | |
| `serviceAccount.create/name/annotations` | `true` | |
| `rbac.create` | `true` | ClusterRole + binding |
| `rbac.management.enabled` | `false` | allow delete pod / scale / restart |
| `cluster.enabled` | `true` | register the in-cluster source |
| `cluster.name` | `in-cluster` | source name |
| `cluster.pollInterval` | `""` | per-source sampling interval |
| `cluster.readOnly` | `null` | auto: `true` unless management RBAC enabled |
| `extraSources` | `[]` | extra `collector.sources` entries (config.yaml keys) |
| `server.readTimeout` | `15s` | |
| `server.publicUrl` | `""` | base URL for notification links |
| `log.level`, `log.format` | `info`, `json` | |
| `collector.statsPollInterval` | `15s` | |
| `collector.scoringInterval`, `.scoringWindow` | `60s`, `24h` | |
| `collector.crashLogs.enabled/tailLines/maxBytes` | `true/200/65536` | |
| `collector.k8sEvents.enabled/normalReasons/excludeNamespaces` | `true/[]/[]` | empty reasons = built-in list |
| `issues.evaluateInterval/degradedAfter/resolveAfter/reopenWindow/includeSystem` | `30s/3m/5m/15m/false` | |
| `notifications.enabled` | `true` | |
| `notifications.channels` | `[]` | declared channels |
| `configInSecret` | `false` | store config.yaml in a Secret |
| `config` | `{}` | raw YAML deep-merged over the generated config |
| `secretKey.value` | `""` | explicit key; empty = generated |
| `secretKey.existingSecret`, `.existingSecretKey` | `""`, `secret-key` | |
| `database.dsn` | `""` | external DSN |
| `database.existingSecret`, `.existingSecretKey` | `""`, `dsn` | |
| `database.maxConns`, `.autoMigrate` | `20`, `true` | |
| `timescaledb.enabled` | `true` | bundled database |
| `timescaledb.image.*` | `timescale/timescaledb:latest-pg16` | |
| `timescaledb.auth.username/database/password` | `kymo/kymo/""` | empty password = generated |
| `timescaledb.persistence.enabled/size/storageClass/accessModes` | `true/10Gi/""/[ReadWriteOnce]` | `-` = no storage class |
| `timescaledb.resources` | 250m/512Mi req, 1Gi limit | |
| `timescaledb.podSecurityContext`, `.securityContext` | seccomp / `{}` | postgres needs chown + privilege drop |
| `timescaledb.podAnnotations/nodeSelector/tolerations/affinity/priorityClassName` | | |
| `service.type/port/annotations` | `ClusterIP/8080/{}` | |
| `ingress.enabled/className/annotations/hosts/tls` | `false/""/{}/[]/[]` | |
| `tests.image.*` | `busybox:1.36` | `helm test` image |
