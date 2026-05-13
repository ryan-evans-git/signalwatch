//go:build duckdb

// Package duckdb registers a DuckDB datasource for sql_returns_rows
// rules. DuckDB is CGO-only, so this build target is gated by the
// `duckdb` build tag; default builds get the stub in `stub.go` and
// stay pure-Go static.
//
// Use:
//
//	import (
//	    "github.com/ryan-evans-git/signalwatch/internal/datasource/duckdb"
//	)
//
//	db, err := duckdb.Open("analytics.duckdb")
//	if err != nil { return err }
//	defer db.Close()
//	registry.Register("analytics", db)
//
// Then a rule with
//
//	{"type":"sql_returns_rows","spec":{"data_source":"analytics","query":"..."}}
//
// runs the configured query against the DuckDB database on the rule's
// schedule.
package duckdb

import (
	"database/sql"
	"errors"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Enabled reports whether the binary was built with the `duckdb` tag.
// True in this file; false in stub.go.
const Enabled = true

// Open opens a DuckDB *sql.DB. An empty dsn opens an anonymous
// in-memory database (useful for tests); a "file:..." or bare path
// opens a file-backed database. Caller is responsible for Close().
//
// Pings the DB before returning so configuration errors (missing
// extension, unwritable path) surface immediately rather than on
// first query.
func Open(dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, errors.New("duckdb: " + err.Error())
	}
	return db, nil
}
