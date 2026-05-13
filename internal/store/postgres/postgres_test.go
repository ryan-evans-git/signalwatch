//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/store/postgres"
	"github.com/ryan-evans-git/signalwatch/internal/store/storetest"
)

// One Postgres container is shared across all subtests in a `go test`
// invocation; each subtest gets a fresh database inside that container
// via CREATE/DROP DATABASE, keeping things isolated without paying for
// a container per test.
var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error
)

const containerImage = "docker.io/postgres:17-alpine"

// containerBaseDSN spins up the shared container (if not already) and
// returns the admin DSN with the default `postgres` database selected.
// Tests should not connect with this DSN directly — they should call
// freshStore to get an isolated DB.
func containerBaseDSN(t *testing.T) string {
	t.Helper()
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		pg, err := tcpg.Run(ctx, containerImage,
			tcpg.WithDatabase("postgres"),
			tcpg.WithUsername("signalwatch"),
			tcpg.WithPassword("signalwatch"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			containerErr = fmt.Errorf("start container: %w", err)
			return
		}
		dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			containerErr = fmt.Errorf("get DSN: %w", err)
			return
		}
		containerDSN = dsn
		// Don't tear the container down at the end of each test — it
		// stays for the lifetime of the test binary. Testcontainers'
		// reaper will clean up at process exit.
	})
	if containerErr != nil {
		t.Skipf("postgres testcontainer unavailable: %v", containerErr)
	}
	return containerDSN
}

// freshStore creates a fresh database inside the shared container,
// connects to it, runs migrations, and registers a cleanup that drops
// the database when the test ends. The result satisfies store.Store.
func freshStore(t *testing.T) store.Store {
	t.Helper()
	base := containerBaseDSN(t)

	dbName := freshDBName(t)

	admin, err := postgres.Open(base)
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	if _, err := admin.DB().ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		_ = admin.Close()
		t.Fatalf("create db: %v", err)
	}
	_ = admin.Close()

	tdsn := swapDB(base, dbName)
	st, err := postgres.Open(tdsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	t.Cleanup(func() {
		_ = st.Close()
		// Re-open admin to drop the test database.
		a, err := postgres.Open(base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.DB().ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})
	return st
}

var dbSeq atomic.Uint64

// freshDBName generates a unique, lowercase, Postgres-safe identifier
// derived from the test name + a per-process sequence.
func freshDBName(t *testing.T) string {
	t.Helper()
	dbSeq.Add(1)
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("sw_%s_%d", safe, dbSeq.Load())
}

// swapDB replaces the database name in a postgres DSN. Works for both
// libpq URI form (postgres://...) and key/value form.
func swapDB(dsn, dbName string) string {
	// Trim any existing path component and replace it.
	q := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		q = dsn[i:]
		dsn = dsn[:i]
	}
	// Find scheme://authority and replace the path.
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		slash := strings.Index(rest, "/")
		var authority string
		if slash >= 0 {
			authority = rest[:slash]
		} else {
			authority = rest
		}
		dsn = dsn[:i+3] + authority + "/" + dbName
	}
	return dsn + q
}

// TestConformance runs the cross-driver behavioral suite against the
// postgres implementation.
func TestConformance(t *testing.T) {
	storetest.RunConformance(t, freshStore)
}

// ---- driver-specific tests ----

func TestOpen_RejectsBadDSN(t *testing.T) {
	_, err := postgres.Open("postgres://no:nope@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatalf("expected error from unreachable DSN")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	st := freshStore(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate (idempotent): %v", err)
	}
}

func TestDB_ReturnsHandle(t *testing.T) {
	st := freshStore(t)
	pgst, ok := st.(*postgres.Store)
	if !ok {
		t.Fatalf("Store type assertion failed")
	}
	if pgst.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}
