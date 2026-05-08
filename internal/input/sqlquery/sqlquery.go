// Package sqlquery exposes a registry of named *sql.DB datasources used by
// SQLReturnsRows rules. The scheduled evaluator looks up the datasource at
// evaluation time and runs the configured query.
//
// The package is intentionally narrow: it does not create database/sql
// drivers itself; callers register an already-opened *sql.DB.
package sqlquery

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Registry maps datasource names to *sql.DB instances.
type Registry struct {
	mu  sync.RWMutex
	dbs map[string]*sql.DB
}

func NewRegistry() *Registry {
	return &Registry{dbs: map[string]*sql.DB{}}
}

func (r *Registry) Register(name string, db *sql.DB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dbs[name] = db
}

func (r *Registry) Get(name string) (*sql.DB, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	db, ok := r.dbs[name]
	return db, ok
}

// CountRows executes query against the registered datasource and returns
// the number of rows. Used by the scheduled evaluator for SQLReturnsRows.
func (r *Registry) CountRows(ctx context.Context, datasource, query string) (int, error) {
	db, ok := r.Get(datasource)
	if !ok {
		return 0, fmt.Errorf("sqlquery: datasource %q not registered", datasource)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}
