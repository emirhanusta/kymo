// Package migrations embeds the SQL schema migrations so the service can apply
// them automatically on startup. Files are applied in lexical order and every
// statement is written to be idempotent, so they are also safe to run by hand
// (psql -f) or via the TimescaleDB image's /docker-entrypoint-initdb.d.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
