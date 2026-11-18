{{/* Chart name. */}}
{{- define "kymo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name (max 63 chars). */}}
{{- define "kymo.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* Name of the bundled TimescaleDB resources. */}}
{{- define "kymo.timescaledb.fullname" -}}
{{- printf "%s-timescaledb" (include "kymo.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kymo.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kymo.labels" -}}
helm.sh/chart: {{ include "kymo.chart" . }}
{{ include "kymo.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kymo.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kymo.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kymo.timescaledb.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kymo.name" . }}-timescaledb
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kymo.timescaledb.labels" -}}
helm.sh/chart: {{ include "kymo.chart" . }}
{{ include "kymo.timescaledb.selectorLabels" . }}
app.kubernetes.io/component: database
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kymo.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kymo.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "kymo.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "kymo.timescaledb.image" -}}
{{- printf "%s:%s" .Values.timescaledb.image.repository .Values.timescaledb.image.tag }}
{{- end }}

{{/* Fail early with a clear message when the database settings are incomplete. */}}
{{- define "kymo.validate" -}}
{{- if and (not .Values.timescaledb.enabled) (not .Values.database.dsn) (not .Values.database.existingSecret) }}
{{- fail "timescaledb.enabled=false: set database.dsn (e.g. postgres://user:pass@host:5432/kymo?sslmode=require) or database.existingSecret (a Secret holding the DSN under database.existingSecretKey)" }}
{{- end }}
{{- if and .Values.timescaledb.enabled (or .Values.database.dsn .Values.database.existingSecret) }}
{{- fail "timescaledb.enabled=true builds the DSN automatically; unset database.dsn / database.existingSecret or set timescaledb.enabled=false" }}
{{- end }}
{{- if and .Values.auth.admin.password (lt (len .Values.auth.admin.password) 12) }}
{{- fail "auth.admin.password must be at least 12 characters (or leave it empty to generate one)" }}
{{- end }}
{{- if and .Values.auth.admin.username (not (regexMatch "^[a-z0-9][a-z0-9._-]{1,63}$" .Values.auth.admin.username)) }}
{{- fail "auth.admin.username must be 2-64 characters: lower-case letters, digits, '.', '_' or '-'" }}
{{- end }}
{{- if and .Values.auth.enabled .Values.auth.oidc.enabled }}
{{- if not .Values.server.publicUrl }}
{{- fail "auth.oidc.enabled=true requires server.publicUrl (the redirect URL is <publicUrl>/api/v1/auth/oidc/callback)" }}
{{- end }}
{{- if or (not .Values.auth.oidc.issuer) (not .Values.auth.oidc.clientId) }}
{{- fail "auth.oidc.enabled=true requires auth.oidc.issuer and auth.oidc.clientId" }}
{{- end }}
{{- if and (not .Values.auth.oidc.clientSecret) (not .Values.auth.oidc.existingSecret) }}
{{- fail "auth.oidc.enabled=true requires auth.oidc.clientSecret or auth.oidc.existingSecret" }}
{{- end }}
{{- end }}
{{- if and .Values.ingress.enabled (not .Values.ingress.hosts) }}
{{- fail "ingress.enabled=true requires at least one entry in ingress.hosts" }}
{{- end }}
{{- end }}

{{/* Name / key of the Secret holding the app's env values. */}}
{{- define "kymo.secretName" -}}
{{- include "kymo.fullname" . }}
{{- end }}

{{- define "kymo.dsnSecretName" -}}
{{- default (include "kymo.secretName" .) .Values.database.existingSecret }}
{{- end }}

{{- define "kymo.dsnSecretKey" -}}
{{- if .Values.database.existingSecret }}{{ .Values.database.existingSecretKey }}{{ else }}database-dsn{{ end }}
{{- end }}

{{- define "kymo.keySecretName" -}}
{{- default (include "kymo.secretName" .) .Values.secretKey.existingSecret }}
{{- end }}

{{- define "kymo.keySecretKey" -}}
{{- if .Values.secretKey.existingSecret }}{{ .Values.secretKey.existingSecretKey }}{{ else }}secret-key{{ end }}
{{- end }}

{{- define "kymo.oidcSecretName" -}}
{{- default (include "kymo.secretName" .) .Values.auth.oidc.existingSecret }}
{{- end }}

{{- define "kymo.oidcSecretKey" -}}
{{- if .Values.auth.oidc.existingSecret }}{{ .Values.auth.oidc.existingSecretKey }}{{ else }}oidc-client-secret{{ end }}
{{- end }}

{{/* Name of the object (ConfigMap or Secret) holding config.yaml. */}}
{{- define "kymo.configName" -}}
{{- printf "%s-config" (include "kymo.fullname" .) }}
{{- end }}

{{/* Render config.yaml from values (with the optional raw override merged in). */}}
{{- define "kymo.configYaml" -}}
{{- $c := .Values.collector -}}
{{- $sources := list -}}
{{- if .Values.cluster.enabled -}}
  {{- $src := dict "name" .Values.cluster.name "type" "kubernetes" -}}
  {{- /* No kubeconfig_path: KYMO falls back to the pod's in-cluster credentials. */ -}}
  {{- if .Values.cluster.pollInterval }}{{- $_ := set $src "poll_interval" .Values.cluster.pollInterval -}}{{- end -}}
  {{- $ro := .Values.cluster.readOnly -}}
  {{- if kindIs "invalid" $ro }}{{- $ro = not .Values.rbac.management.enabled -}}{{- end -}}
  {{- $_ := set $src "read_only" $ro -}}
  {{- $sources = append $sources $src -}}
{{- end -}}
{{- $sources = concat $sources .Values.extraSources -}}
{{- $k8sEvents := dict "enabled" $c.k8sEvents.enabled "exclude_namespaces" ($c.k8sEvents.excludeNamespaces | default list) -}}
{{- if $c.k8sEvents.normalReasons }}{{- $_ := set $k8sEvents "normal_reasons" $c.k8sEvents.normalReasons -}}{{- end -}}
{{- $cfg := dict
  "server" (dict "address" (printf ":%v" .Values.containerPort) "read_timeout" .Values.server.readTimeout "public_url" (.Values.server.publicUrl | default "")
    "allowed_origins" (.Values.auth.allowedOrigins | default list) "trusted_proxies" (.Values.auth.trustedProxies | default list))
  "auth" (dict "enabled" .Values.auth.enabled "session_idle_timeout" .Values.auth.sessionIdleTimeout "session_max_age" .Values.auth.sessionMaxAge
    "oidc" (dict
      "enabled" .Values.auth.oidc.enabled
      "name" .Values.auth.oidc.name
      "issuer" .Values.auth.oidc.issuer
      "client_id" .Values.auth.oidc.clientId
      "scopes" .Values.auth.oidc.scopes
      "allowed_domains" (.Values.auth.oidc.allowedDomains | default list)
      "groups_claim" .Values.auth.oidc.groupsClaim
      "role_mapping" (.Values.auth.oidc.roleMapping | default dict)
      "default_role" .Values.auth.oidc.defaultRole
      "auto_create" .Values.auth.oidc.autoCreate
      "disable_password_login" .Values.auth.oidc.disablePasswordLogin))
  "database" (dict "max_conns" .Values.database.maxConns "auto_migrate" .Values.database.autoMigrate)
  "collector" (dict
    "stats_poll_interval" $c.statsPollInterval
    "scoring_interval" $c.scoringInterval
    "scoring_window" $c.scoringWindow
    "crash_logs" (dict "enabled" $c.crashLogs.enabled "tail_lines" $c.crashLogs.tailLines "max_bytes" $c.crashLogs.maxBytes)
    "k8s_events" $k8sEvents
    "sources" $sources)
  "issues" (dict
    "evaluate_interval" .Values.issues.evaluateInterval
    "degraded_after" .Values.issues.degradedAfter
    "resolve_after" .Values.issues.resolveAfter
    "reopen_window" .Values.issues.reopenWindow
    "include_system" .Values.issues.includeSystem
    "error_rate_threshold" (.Values.issues.errorRateThreshold | default 20))
  "log_capture" (dict
    "enabled" .Values.logCapture.enabled
    "max_streams_per_source" .Values.logCapture.maxStreamsPerSource
    "max_lines_per_minute" .Values.logCapture.maxLinesPerMinute
    "samples_per_signature" .Values.logCapture.samplesPerSignature
    "retention" .Values.logCapture.retention
    "extra_patterns" (.Values.logCapture.extraPatterns | default list)
    "ignore_patterns" (.Values.logCapture.ignorePatterns | default list)
    "targets" (.Values.logCapture.targets | default list))
  "notifications" (dict "enabled" .Values.notifications.enabled "channels" .Values.notifications.channels)
  "log" (dict "level" .Values.log.level "format" .Values.log.format) -}}
{{- $cfg = mustMergeOverwrite $cfg (deepCopy .Values.config) -}}
{{- toYaml $cfg -}}
{{- end }}
