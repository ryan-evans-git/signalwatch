//go:build !duckdb

package duckdb_test

import (
	"errors"
	"testing"

	"github.com/ryan-evans-git/signalwatch/internal/datasource/duckdb"
)

// In the default no-tag build, Enabled is false and Open always
// returns ErrDisabled. Callers can use Enabled to decide whether to
// invoke Open in the first place.

func TestStub_EnabledIsFalse(t *testing.T) {
	if duckdb.Enabled {
		t.Fatalf("Enabled should be false in stub build")
	}
}

func TestStub_OpenReturnsErrDisabled(t *testing.T) {
	for _, dsn := range []string{"", ":memory:", "analytics.duckdb"} {
		db, err := duckdb.Open(dsn)
		if !errors.Is(err, duckdb.ErrDisabled) {
			t.Errorf("Open(%q): want ErrDisabled, got %v", dsn, err)
		}
		if db != nil {
			t.Errorf("Open(%q): want nil db, got %v", dsn, db)
		}
	}
}

func TestStub_ErrDisabledMessage(t *testing.T) {
	if got := duckdb.ErrDisabled.Error(); got == "" {
		t.Fatalf("ErrDisabled message should not be empty")
	}
}
