//go:build integration

package mysql_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // wait.ForSQL needs a registered driver

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/store/mysql"
	"github.com/ryan-evans-git/signalwatch/internal/store/storetest"
)

// One MySQL container is shared across all subtests in a `go test`
// invocation; each subtest gets a fresh database via CREATE/DROP
// DATABASE, keeping things isolated without paying for a container per
// test.
var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error
)

const containerImage = "docker.io/mysql:8.4"

// containerBaseDSN spins up the shared container (if not already) and
// returns the root DSN with the default schema selected. Tests should
// not connect with this DSN directly — they should call freshStore.
func containerBaseDSN(t *testing.T) string {
	t.Helper()
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		// mysql:8.4 restarts the server after running init scripts, so a
		// log-occurrence wait can return between the initial boot and
		// the post-init restart. wait.ForSQL probes with an actual
		// SELECT 1 so it only succeeds once the server is genuinely
		// accepting queries on the post-restart instance.
		c, err := tcmysql.Run(ctx, containerImage,
			tcmysql.WithDatabase("signalwatch"),
			tcmysql.WithUsername("signalwatch"),
			tcmysql.WithPassword("signalwatch"),
			testcontainers.WithWaitStrategy(
				wait.ForSQL("3306/tcp", "mysql", func(host string, port string) string {
					return fmt.Sprintf("signalwatch:signalwatch@tcp(%s:%s)/signalwatch?parseTime=true", host, port)
				}).WithStartupTimeout(3*time.Minute).WithPollInterval(2*time.Second),
			),
		)
		if err != nil {
			containerErr = fmt.Errorf("start container: %w", err)
			return
		}
		dsn, err := c.ConnectionString(ctx)
		if err != nil {
			containerErr = fmt.Errorf("get DSN: %w", err)
			return
		}
		containerDSN = dsn
		// Container stays for the lifetime of the test binary; the
		// testcontainers reaper cleans up at process exit.
	})
	if containerErr != nil {
		t.Skipf("mysql testcontainer unavailable: %v", containerErr)
	}
	return containerDSN
}

// freshStore creates a fresh database inside the shared container,
// connects to it, runs migrations, and registers cleanup. Result
// satisfies store.Store.
func freshStore(t *testing.T) store.Store {
	t.Helper()
	base := containerBaseDSN(t)
	dbName := freshDBName(t)

	admin, err := mysql.Open(base)
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	if _, err := admin.DB().ExecContext(context.Background(), "CREATE DATABASE "+dbName); err != nil {
		_ = admin.Close()
		t.Fatalf("create db: %v", err)
	}
	_ = admin.Close()

	tdsn := swapDB(base, dbName)
	st, err := mysql.Open(tdsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	t.Cleanup(func() {
		_ = st.Close()
		a, err := mysql.Open(base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.DB().ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+dbName)
	})
	return st
}

var dbSeq atomic.Uint64

// freshDBName generates a unique MySQL-safe identifier (≤ 64 chars).
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

// swapDB replaces the database name in a go-sql-driver/mysql DSN. DSNs
// look like "user:pass@tcp(host:port)/dbname?params". We split on the
// `/` between the host and dbname.
func swapDB(dsn, dbName string) string {
	q := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		q = dsn[i:]
		dsn = dsn[:i]
	}
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		dsn = dsn[:i+1] + dbName
	}
	return dsn + q
}

// TestConformance runs the cross-driver behavioral suite against the
// MySQL implementation.
func TestConformance(t *testing.T) {
	storetest.RunConformance(t, freshStore)
}

// ---- driver-specific tests ----

func TestOpen_RejectsBadDSN(t *testing.T) {
	_, err := mysql.Open("nope:nope@tcp(127.0.0.1:1)/none?timeout=1s")
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
	mst, ok := st.(*mysql.Store)
	if !ok {
		t.Fatalf("Store type assertion failed")
	}
	if mst.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}
