package timescale

// Integration-test harness. The tests in *_integration_test.go run against a
// real TimescaleDB and are skipped unless KYMO_TEST_DSN is set (see `make
// test-db`). TestMain creates a private kymo_test_<random> database on that
// server, migrates it, and drops it again when the run ends.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/migrations"
)

var (
	testDB      *DB    // shared, migrated database; nil when integration tests are disabled
	testAdminDS string // DSN of the server's maintenance database
)

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	testAdminDS = os.Getenv("KYMO_TEST_DSN")
	if testAdminDS == "" {
		return m.Run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name, err := createTestDatabase(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		return 1
	}
	defer dropTestDatabase(name)

	db, err := New(ctx, dsnForDatabase(name), 8, zap.NewNop())
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		return 1
	}
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "integration migrate:", err)
		return 1
	}
	testDB = db
	code := m.Run()
	db.Close()
	return code
}

func randomDBName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "kymo_test_" + hex.EncodeToString(b)
}

func createTestDatabase(ctx context.Context) (string, error) {
	conn, err := pgx.Connect(ctx, testAdminDS)
	if err != nil {
		return "", fmt.Errorf("connect KYMO_TEST_DSN: %w", err)
	}
	defer conn.Close(ctx)
	name := randomDBName()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("create database: %w", err)
	}
	return name, nil
}

func dropTestDatabase(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, testAdminDS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drop database: connect:", err)
		return
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		fmt.Fprintln(os.Stderr, "drop database:", err)
	}
}

// dsnForDatabase returns testAdminDS pointed at another database.
func dsnForDatabase(name string) string {
	if u, err := url.Parse(testAdminDS); err == nil && u.Scheme != "" {
		u.Path = "/" + name
		return u.String()
	}
	return testAdminDS + " dbname=" + name // keyword/value form
}

// newIsolatedDB creates and opens an extra empty database (not migrated).
func newIsolatedDB(t *testing.T) *DB {
	t.Helper()
	requireDB(t)
	ctx := context.Background()
	name, err := createTestDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := New(ctx, dsnForDatabase(name), 4, zap.NewNop())
	if err != nil {
		dropTestDatabase(name)
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); dropTestDatabase(name) })
	return db
}

func requireDB(t *testing.T) {
	t.Helper()
	if testDB == nil {
		t.Skip("KYMO_TEST_DSN not set; skipping database integration test")
	}
}

// reset empties every KYMO table and returns the shared DB.
func reset(t *testing.T) *DB {
	t.Helper()
	requireDB(t)
	_, err := testDB.pool.Exec(context.Background(), `TRUNCATE containers, lifecycle_events,
  resource_samples, stability_scores, workload_scores, cronjob_executions, sources, crash_logs, k8s_events, issues, notification_channels, notification_deliveries,
  log_capture_targets, error_signatures, error_counts, error_occurrences CASCADE`)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	return testDB
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// base is an hour-aligned instant a few hours in the past (recent enough that
// the crash_logs retention policy never reaps test rows).
var base = time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)

func at(d time.Duration) time.Time { return base.Add(d) }

// newContainer returns a fully populated Kubernetes container fixture.
func newContainer(id string) *model.Container {
	return &model.Container{
		ID: id, Runtime: model.RuntimeKubernetes, SourceName: "src1",
		Name: "name-" + id, Image: "img:1", Namespace: "ns", NodeName: "node-1",
		FirstSeenAt: base, LastSeenAt: base, CurrentStatus: model.StatusRunning,
		WorkloadKind: model.WorkloadDeployment, WorkloadName: "web",
	}
}

func mustUpsert(t *testing.T, db *DB, c *model.Container) *model.Container {
	t.Helper()
	out, err := db.Containers().Upsert(context.Background(), c, false)
	if err != nil {
		t.Fatalf("upsert %s: %v", c.ID, err)
	}
	return out
}

func mustFind(t *testing.T, db *DB, id string) *model.Container {
	t.Helper()
	c, err := db.Containers().FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("find %s: %v", id, err)
	}
	if c == nil {
		t.Fatalf("container %s not found", id)
	}
	return c
}

func ids(cs []*model.Container) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joined(s []string) string { return "[" + strings.Join(s, " ") + "]" }

func count(t *testing.T, db *DB, table, where string, args ...any) int {
	t.Helper()
	var n int
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	if err := db.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }
