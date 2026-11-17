package timescale

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"go.uber.org/zap"
)

const createMigrationsTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT        PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`

// Migrate applies every *.sql file in migrationsFS that has not been recorded in
// the schema_migrations table, in lexical filename order.
//
// Statements are executed one at a time outside an explicit transaction because
// some TimescaleDB DDL (e.g. continuous aggregates) refuses to run inside one.
// All shipped migrations are idempotent, so a database that was initialised by
// hand or via docker-entrypoint-initdb.d is simply brought up to date.
func (db *DB) Migrate(ctx context.Context, migrationsFS fs.FS) error {
	if _, err := db.pool.Exec(ctx, createMigrationsTableSQL); err != nil {
		return fmt.Errorf("timescale: create schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "*.sql")
	if err != nil {
		return fmt.Errorf("timescale: list migrations: %w", err)
	}
	sort.Strings(files)

	for _, name := range files {
		var exists bool
		if err := db.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("timescale: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return fmt.Errorf("timescale: read migration %s: %w", name, err)
		}
		for i, stmt := range splitSQLStatements(string(body)) {
			if _, err := db.pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("timescale: migration %s statement %d: %w", name, i+1, err)
			}
		}
		if _, err := db.pool.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, name,
		); err != nil {
			return fmt.Errorf("timescale: record migration %s: %w", name, err)
		}
		db.log.Info("migration applied", zap.String("version", name))
	}
	return nil
}

// splitSQLStatements splits a SQL script on top-level semicolons. It understands
// single-quoted strings, double-quoted identifiers, dollar-quoted bodies ($$ or
// $tag$), and "--" / "/* */" comments, which is sufficient for the shipped
// migrations. Empty and comment-only statements are dropped.
func splitSQLStatements(script string) []string {
	var (
		stmts   []string
		current strings.Builder
		i       int
	)
	flush := func() {
		if s := strings.TrimSpace(current.String()); s != "" && !commentOnly(s) {
			stmts = append(stmts, s)
		}
		current.Reset()
	}

	for i < len(script) {
		ch := script[i]
		switch {
		case ch == '-' && i+1 < len(script) && script[i+1] == '-':
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				end = len(script) - i
			}
			current.WriteString(script[i : i+end])
			i += end
		case ch == '/' && i+1 < len(script) && script[i+1] == '*':
			end := strings.Index(script[i+2:], "*/")
			if end < 0 {
				end = len(script) - i - 2
			} else {
				end += 2
			}
			current.WriteString(script[i : i+2+end])
			i += 2 + end
		case ch == '\'' || ch == '"':
			j := i + 1
			for j < len(script) {
				if script[j] == ch {
					if j+1 < len(script) && script[j+1] == ch { // escaped quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(script))
			current.WriteString(script[i:end])
			i = end
		case ch == '$':
			if tag, ok := dollarTag(script[i:]); ok {
				end := strings.Index(script[i+len(tag):], tag)
				if end < 0 {
					end = len(script) - i - len(tag)
				} else {
					end += len(tag)
				}
				current.WriteString(script[i : i+len(tag)+end])
				i += len(tag) + end
				continue
			}
			current.WriteByte(ch)
			i++
		case ch == ';':
			flush()
			i++
		default:
			current.WriteByte(ch)
			i++
		}
	}
	flush()
	return stmts
}

// dollarTag returns the dollar-quote opening tag ($$ or $name$) at the start of
// s, if any.
func dollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == '$' {
			return s[:j+1], true
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 1 && c >= '0' && c <= '9') {
			return "", false
		}
	}
	return "", false
}

// commentOnly reports whether a statement consists solely of SQL comments.
func commentOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return false
		}
	}
	return true
}
