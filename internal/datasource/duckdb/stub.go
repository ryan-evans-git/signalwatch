//go:build !duckdb

// Package duckdb is the no-CGO stub. Real implementation lives in
// duckdb.go behind the `duckdb` build tag; the default build doesn't
// link DuckDB so the signalwatch binary stays a pure-Go single file.
//
// Callers can probe `duckdb.Enabled` to decide whether to attempt an
// Open(). The bundled `signalwatch` binary doesn't register DuckDB
// datasources itself — that's a programmatic step for embedders, or
// for a future build that's distributed with the tag set.
package duckdb

import (
	"database/sql"
	"errors"
)

// Enabled reports whether the binary was built with the `duckdb` tag.
// False in this stub; true in duckdb.go.
const Enabled = false

// ErrDisabled is returned by Open() when the binary was built without
// the `duckdb` tag. The plain text matches the v2 stub error so it's
// trivial to grep.
var ErrDisabled = errors.New("duckdb: binary built without -tags=duckdb")

// Open is the stub form. It always returns ErrDisabled — DuckDB needs
// CGO and isn't linked into the default build.
func Open(_ string) (*sql.DB, error) {
	return nil, ErrDisabled
}
