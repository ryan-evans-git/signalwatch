//go:build duckdb

package duckdb_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ryan-evans-git/signalwatch/internal/datasource/duckdb"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
)

// In a CGO-enabled build with the `duckdb` tag, Enabled is true and
// Open returns a working *sql.DB.

func TestDuckDB_EnabledIsTrue(t *testing.T) {
	if !duckdb.Enabled {
		t.Fatalf("Enabled should be true in duckdb-tagged build")
	}
}

func TestDuckDB_OpenInMemoryRoundTrip(t *testing.T) {
	for _, dsn := range []string{"", ":memory:"} {
		db, err := duckdb.Open(dsn)
		if err != nil {
			t.Fatalf("Open(%q): %v", dsn, err)
		}
		t.Cleanup(func() { _ = db.Close() })

		var n int
		if err := db.QueryRow(`SELECT 1 + 2`).Scan(&n); err != nil {
			t.Fatalf("QueryRow: %v", err)
		}
		if n != 3 {
			t.Errorf("dsn=%q: SELECT 1+2 = %d, want 3", dsn, n)
		}
	}
}

func TestDuckDB_OpenFileBacked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.duckdb")
	db, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE events (id INTEGER, level TEXT)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO events VALUES (1, 'ERROR'), (2, 'INFO')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE level = 'ERROR'`).Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 1 {
		t.Errorf("COUNT(*) = %d, want 1", count)
	}
}

func TestDuckDB_OpenRejectsUnreachablePath(t *testing.T) {
	// Path in a non-existent directory: DuckDB refuses to write to it.
	_, err := duckdb.Open("/no-such-dir/nope.duckdb")
	if err == nil {
		t.Fatalf("Open(/no-such-dir/...): want error")
	}
}

// End-to-end: register the DuckDB *sql.DB in the sqlquery.Registry
// and use it to count rows like a sql_returns_rows rule would.
func TestDuckDB_SQLQueryRegistryIntegration(t *testing.T) {
	db, err := duckdb.Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE incidents (id INTEGER, severity TEXT)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO incidents VALUES (1,'critical'), (2,'critical'), (3,'info')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	reg := sqlquery.NewRegistry()
	reg.Register("analytics", db)

	n, err := reg.CountRows(context.Background(), "analytics",
		`SELECT id FROM incidents WHERE severity = 'critical'`)
	if err != nil {
		t.Fatalf("CountRows: %v", err)
	}
	if n != 2 {
		t.Errorf("CountRows: want 2, got %d", n)
	}
}
