// Package sqlite provides the default Store implementation backed by SQLite
// via the pure-Go modernc.org/sqlite driver.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite-backed implementation of store.Store.
type Store struct {
	db *sql.DB
}

// Open creates a new SQLite store. dsn is forwarded to database/sql.
// A typical value is "file:signalwatch.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)".
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single connection avoids cross-connection locking issues with SQLite.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// DB returns the underlying *sql.DB. Used for tests and for SQLEvaluator
// rules that want to hit the same store as a datasource.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Rules() store.RuleRepo                 { return &ruleRepo{db: s.db} }
func (s *Store) Subscribers() store.SubscriberRepo     { return &subscriberRepo{db: s.db} }
func (s *Store) Subscriptions() store.SubscriptionRepo { return &subscriptionRepo{db: s.db} }
func (s *Store) Incidents() store.IncidentRepo         { return &incidentRepo{db: s.db} }
func (s *Store) Notifications() store.NotificationRepo { return &notificationRepo{db: s.db} }
func (s *Store) LiveStates() store.LiveStateRepo       { return &liveStateRepo{db: s.db} }
func (s *Store) IncidentSubStates() store.IncidentSubStateRepo {
	return &incidentSubStateRepo{db: s.db}
}

// ----- helpers -----

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func parseJSON(s string, v any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

// ----- rules -----

type ruleRepo struct{ db *sql.DB }

func (r *ruleRepo) Create(ctx context.Context, x *rule.Rule) error {
	condJSON, err := x.Condition.MarshalCondition()
	if err != nil {
		return err
	}
	now := time.Now()
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now
	}
	x.UpdatedAt = now
	_, err = r.db.ExecContext(ctx, `INSERT INTO rules
        (id, name, description, enabled, severity, labels, input_ref, condition, schedule_ns, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.Name, x.Description, boolInt(x.Enabled), string(x.Severity), mustJSON(x.Labels), x.InputRef, string(condJSON), int64(x.Schedule),
		x.CreatedAt.UnixMilli(), x.UpdatedAt.UnixMilli())
	return err
}

func (r *ruleRepo) Update(ctx context.Context, x *rule.Rule) error {
	condJSON, err := x.Condition.MarshalCondition()
	if err != nil {
		return err
	}
	x.UpdatedAt = time.Now()
	_, err = r.db.ExecContext(ctx, `UPDATE rules
        SET name = ?, description = ?, enabled = ?, severity = ?, labels = ?, input_ref = ?, condition = ?, schedule_ns = ?, updated_at = ?
        WHERE id = ?`,
		x.Name, x.Description, boolInt(x.Enabled), string(x.Severity), mustJSON(x.Labels), x.InputRef, string(condJSON), int64(x.Schedule),
		x.UpdatedAt.UnixMilli(), x.ID)
	return err
}

func (r *ruleRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
	return err
}

func (r *ruleRepo) Get(ctx context.Context, id string) (*rule.Rule, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, name, description, enabled, severity, labels, input_ref, condition, schedule_ns, created_at, updated_at FROM rules WHERE id = ?`, id)
	return scanRule(row)
}

func (r *ruleRepo) List(ctx context.Context) ([]*rule.Rule, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, description, enabled, severity, labels, input_ref, condition, schedule_ns, created_at, updated_at FROM rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

func (r *ruleRepo) ListByInput(ctx context.Context, inputRef string) ([]*rule.Rule, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, description, enabled, severity, labels, input_ref, condition, schedule_ns, created_at, updated_at FROM rules WHERE input_ref = ? AND enabled = 1`, inputRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

type rowScanner interface {
	Scan(...any) error
}

func scanRule(row rowScanner) (*rule.Rule, error) {
	var (
		id, name, desc, severity, labelsJSON, inputRef, condJSON string
		enabled                                                  int
		scheduleNS, createdMS, updatedMS                         int64
	)
	if err := row.Scan(&id, &name, &desc, &enabled, &severity, &labelsJSON, &inputRef, &condJSON, &scheduleNS, &createdMS, &updatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	cond, err := rule.UnmarshalCondition([]byte(condJSON))
	if err != nil {
		return nil, err
	}
	r := &rule.Rule{
		ID:          id,
		Name:        name,
		Description: desc,
		Enabled:     enabled != 0,
		Severity:    rule.Severity(severity),
		InputRef:    inputRef,
		Condition:   cond,
		Schedule:    time.Duration(scheduleNS),
		CreatedAt:   time.UnixMilli(createdMS),
		UpdatedAt:   time.UnixMilli(updatedMS),
	}
	_ = parseJSON(labelsJSON, &r.Labels)
	return r, nil
}

func scanRules(rows *sql.Rows) ([]*rule.Rule, error) {
	var out []*rule.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// ----- subscribers -----

type subscriberRepo struct{ db *sql.DB }

func (r *subscriberRepo) Create(ctx context.Context, s *subscriber.Subscriber) error {
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `INSERT INTO subscribers (id, name, channels, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		s.ID, s.Name, mustJSON(s.Channels), s.CreatedAt.UnixMilli(), s.UpdatedAt.UnixMilli())
	return err
}

func (r *subscriberRepo) Update(ctx context.Context, s *subscriber.Subscriber) error {
	s.UpdatedAt = time.Now()
	_, err := r.db.ExecContext(ctx, `UPDATE subscribers SET name = ?, channels = ?, updated_at = ? WHERE id = ?`,
		s.Name, mustJSON(s.Channels), s.UpdatedAt.UnixMilli(), s.ID)
	return err
}

func (r *subscriberRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM subscribers WHERE id = ?`, id)
	return err
}

func (r *subscriberRepo) Get(ctx context.Context, id string) (*subscriber.Subscriber, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, name, channels, created_at, updated_at FROM subscribers WHERE id = ?`, id)
	return scanSubscriber(row)
}

func (r *subscriberRepo) List(ctx context.Context) ([]*subscriber.Subscriber, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, channels, created_at, updated_at FROM subscribers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*subscriber.Subscriber
	for rows.Next() {
		s, err := scanSubscriber(rows)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

func scanSubscriber(row rowScanner) (*subscriber.Subscriber, error) {
	var (
		id, name, channels   string
		createdMS, updatedMS int64
	)
	if err := row.Scan(&id, &name, &channels, &createdMS, &updatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s := &subscriber.Subscriber{
		ID: id, Name: name,
		CreatedAt: time.UnixMilli(createdMS),
		UpdatedAt: time.UnixMilli(updatedMS),
	}
	_ = parseJSON(channels, &s.Channels)
	return s, nil
}

// ----- subscriptions -----

type subscriptionRepo struct{ db *sql.DB }

func (r *subscriptionRepo) Create(ctx context.Context, s *subscriber.Subscription) error {
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `INSERT INTO subscriptions
        (id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.SubscriberID, nullable(s.RuleID), mustJSON(s.LabelSelector), int64(s.Dwell), int64(s.RepeatInterval), boolInt(s.NotifyOnResolve), mustJSON(s.ChannelFilter), s.CreatedAt.UnixMilli(), s.UpdatedAt.UnixMilli())
	return err
}

func (r *subscriptionRepo) Update(ctx context.Context, s *subscriber.Subscription) error {
	s.UpdatedAt = time.Now()
	_, err := r.db.ExecContext(ctx, `UPDATE subscriptions
        SET subscriber_id = ?, rule_id = ?, label_selector = ?, dwell_ns = ?, repeat_interval_ns = ?, notify_on_resolve = ?, channel_filter = ?, updated_at = ?
        WHERE id = ?`,
		s.SubscriberID, nullable(s.RuleID), mustJSON(s.LabelSelector), int64(s.Dwell), int64(s.RepeatInterval), boolInt(s.NotifyOnResolve), mustJSON(s.ChannelFilter), s.UpdatedAt.UnixMilli(), s.ID)
	return err
}

func (r *subscriptionRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, id)
	return err
}

func (r *subscriptionRepo) Get(ctx context.Context, id string) (*subscriber.Subscription, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at FROM subscriptions WHERE id = ?`, id)
	return scanSubscription(row)
}

func (r *subscriptionRepo) List(ctx context.Context) ([]*subscriber.Subscription, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at FROM subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (r *subscriptionRepo) ListForRule(ctx context.Context, ruleID string, labels map[string]string) ([]*subscriber.Subscription, error) {
	// Step 1: direct rule_id matches.
	rows, err := r.db.QueryContext(ctx, `SELECT id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at FROM subscriptions WHERE rule_id = ?`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanSubscriptions(rows)
	if err != nil {
		return nil, err
	}

	// Step 2: label-selector matches. Done in-memory; subscriber counts are
	// expected to be small for v0.1.
	rows2, err := r.db.QueryContext(ctx, `SELECT id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at FROM subscriptions WHERE rule_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	all, err := scanSubscriptions(rows2)
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		if matchLabels(s.LabelSelector, labels) {
			out = append(out, s)
		}
	}
	return out, nil
}

func matchLabels(selector, actual map[string]string) bool {
	if len(selector) == 0 {
		return false // an empty selector should not match every rule by accident
	}
	for k, v := range selector {
		if actual[k] != v {
			return false
		}
	}
	return true
}

func scanSubscription(row rowScanner) (*subscriber.Subscription, error) {
	var (
		id, subID, labelSelector, channelFilter string
		ruleID                                  sql.NullString
		dwellNS, repeatNS, createdMS, updatedMS int64
		notifyOnResolve                         int
	)
	if err := row.Scan(&id, &subID, &ruleID, &labelSelector, &dwellNS, &repeatNS, &notifyOnResolve, &channelFilter, &createdMS, &updatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s := &subscriber.Subscription{
		ID:              id,
		SubscriberID:    subID,
		Dwell:           time.Duration(dwellNS),
		RepeatInterval:  time.Duration(repeatNS),
		NotifyOnResolve: notifyOnResolve != 0,
		CreatedAt:       time.UnixMilli(createdMS),
		UpdatedAt:       time.UnixMilli(updatedMS),
	}
	if ruleID.Valid {
		s.RuleID = ruleID.String
	}
	_ = parseJSON(labelSelector, &s.LabelSelector)
	_ = parseJSON(channelFilter, &s.ChannelFilter)
	return s, nil
}

func scanSubscriptions(rows *sql.Rows) ([]*subscriber.Subscription, error) {
	var out []*subscriber.Subscription
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// ----- incidents -----

type incidentRepo struct{ db *sql.DB }

func (r *incidentRepo) Open(ctx context.Context, inc *subscriber.Incident) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO incidents (id, rule_id, triggered_at, resolved_at, last_value) VALUES (?, ?, ?, NULL, ?)`,
		inc.ID, inc.RuleID, inc.TriggeredAt.UnixMilli(), inc.LastValue)
	return err
}

func (r *incidentRepo) Resolve(ctx context.Context, id string, resolvedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE incidents SET resolved_at = ? WHERE id = ?`, resolvedAt, id)
	return err
}

func (r *incidentRepo) Get(ctx context.Context, id string) (*subscriber.Incident, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, rule_id, triggered_at, resolved_at, last_value FROM incidents WHERE id = ?`, id)
	return scanIncident(row)
}

func (r *incidentRepo) List(ctx context.Context, limit int) ([]*subscriber.Incident, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, rule_id, triggered_at, resolved_at, last_value FROM incidents ORDER BY triggered_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIncidents(rows)
}

func (r *incidentRepo) ListForRule(ctx context.Context, ruleID string, limit int) ([]*subscriber.Incident, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, rule_id, triggered_at, resolved_at, last_value FROM incidents WHERE rule_id = ? ORDER BY triggered_at DESC LIMIT ?`, ruleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIncidents(rows)
}

func scanIncident(row rowScanner) (*subscriber.Incident, error) {
	var (
		id, ruleID, lastValue string
		triggeredMS           int64
		resolvedMS            sql.NullInt64
	)
	if err := row.Scan(&id, &ruleID, &triggeredMS, &resolvedMS, &lastValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	inc := &subscriber.Incident{
		ID:          id,
		RuleID:      ruleID,
		TriggeredAt: time.UnixMilli(triggeredMS),
		LastValue:   lastValue,
	}
	if resolvedMS.Valid {
		inc.ResolvedAt = time.UnixMilli(resolvedMS.Int64)
	}
	return inc, nil
}

func scanIncidents(rows *sql.Rows) ([]*subscriber.Incident, error) {
	var out []*subscriber.Incident
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		if i != nil {
			out = append(out, i)
		}
	}
	return out, rows.Err()
}

// ----- notifications -----

type notificationRepo struct{ db *sql.DB }

func (r *notificationRepo) Record(ctx context.Context, n *subscriber.Notification) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO notifications
        (id, incident_id, subscription_id, subscriber_id, channel, address, kind, sent_at, status, error)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.IncidentID, n.SubscriptionID, n.SubscriberID, n.Channel, n.Address, string(n.Kind), n.SentAt.UnixMilli(), n.Status, n.Error)
	return err
}

func (r *notificationRepo) ListForIncident(ctx context.Context, incidentID string) ([]*subscriber.Notification, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, incident_id, subscription_id, subscriber_id, channel, address, kind, sent_at, status, error FROM notifications WHERE incident_id = ? ORDER BY sent_at`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotifications(rows)
}

func (r *notificationRepo) List(ctx context.Context, limit int) ([]*subscriber.Notification, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, incident_id, subscription_id, subscriber_id, channel, address, kind, sent_at, status, error FROM notifications ORDER BY sent_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotifications(rows)
}

func scanNotifications(rows *sql.Rows) ([]*subscriber.Notification, error) {
	var out []*subscriber.Notification
	for rows.Next() {
		var (
			id, incID, subID, subscriberID, channel, address, kind, status string
			errStr                                                         sql.NullString
			sentMS                                                         int64
		)
		if err := rows.Scan(&id, &incID, &subID, &subscriberID, &channel, &address, &kind, &sentMS, &status, &errStr); err != nil {
			return nil, err
		}
		n := &subscriber.Notification{
			ID: id, IncidentID: incID, SubscriptionID: subID, SubscriberID: subscriberID,
			Channel: channel, Address: address, Kind: subscriber.NotificationKind(kind),
			SentAt: time.UnixMilli(sentMS), Status: status,
		}
		if errStr.Valid {
			n.Error = errStr.String
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ----- live states -----

type liveStateRepo struct{ db *sql.DB }

func (r *liveStateRepo) Get(ctx context.Context, ruleID string) (*rule.LiveState, error) {
	row := r.db.QueryRowContext(ctx, `SELECT rule_id, state, triggered_at, last_eval_at, last_value, last_error, incident_id FROM live_states WHERE rule_id = ?`, ruleID)
	var (
		id, state, lv, le   string
		incID               sql.NullString
		triggeredMS, evalMS int64
	)
	if err := row.Scan(&id, &state, &triggeredMS, &evalMS, &lv, &le, &incID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ls := &rule.LiveState{
		RuleID: id, State: rule.State(state), LastValue: lv, LastError: le,
	}
	if triggeredMS > 0 {
		ls.TriggeredAt = time.UnixMilli(triggeredMS)
	}
	if evalMS > 0 {
		ls.LastEvalAt = time.UnixMilli(evalMS)
	}
	if incID.Valid {
		ls.IncidentID = incID.String
	}
	return ls, nil
}

func (r *liveStateRepo) Upsert(ctx context.Context, s *rule.LiveState) error {
	var trig int64
	if !s.TriggeredAt.IsZero() {
		trig = s.TriggeredAt.UnixMilli()
	}
	var eval int64
	if !s.LastEvalAt.IsZero() {
		eval = s.LastEvalAt.UnixMilli()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO live_states (rule_id, state, triggered_at, last_eval_at, last_value, last_error, incident_id)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(rule_id) DO UPDATE SET
            state=excluded.state,
            triggered_at=excluded.triggered_at,
            last_eval_at=excluded.last_eval_at,
            last_value=excluded.last_value,
            last_error=excluded.last_error,
            incident_id=excluded.incident_id`,
		s.RuleID, string(s.State), trig, eval, s.LastValue, s.LastError, nullable(s.IncidentID))
	return err
}

func (r *liveStateRepo) List(ctx context.Context) ([]*rule.LiveState, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT rule_id, state, triggered_at, last_eval_at, last_value, last_error, incident_id FROM live_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*rule.LiveState
	for rows.Next() {
		var (
			id, state, lv, le   string
			incID               sql.NullString
			triggeredMS, evalMS int64
		)
		if err := rows.Scan(&id, &state, &triggeredMS, &evalMS, &lv, &le, &incID); err != nil {
			return nil, err
		}
		ls := &rule.LiveState{RuleID: id, State: rule.State(state), LastValue: lv, LastError: le}
		if triggeredMS > 0 {
			ls.TriggeredAt = time.UnixMilli(triggeredMS)
		}
		if evalMS > 0 {
			ls.LastEvalAt = time.UnixMilli(evalMS)
		}
		if incID.Valid {
			ls.IncidentID = incID.String
		}
		out = append(out, ls)
	}
	return out, rows.Err()
}

// ----- incident sub states -----

type incidentSubStateRepo struct{ db *sql.DB }

func (r *incidentSubStateRepo) Get(ctx context.Context, incidentID, subscriptionID string) (*subscriber.IncidentSubState, error) {
	row := r.db.QueryRowContext(ctx, `SELECT incident_id, subscription_id, last_notified_at, notify_count, resolution_sent FROM incident_sub_states WHERE incident_id = ? AND subscription_id = ?`, incidentID, subscriptionID)
	var (
		incID, subID    string
		lastMS          int64
		count, resolved int
	)
	if err := row.Scan(&incID, &subID, &lastMS, &count, &resolved); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s := &subscriber.IncidentSubState{
		IncidentID:     incID,
		SubscriptionID: subID,
		NotifyCount:    count,
		ResolutionSent: resolved != 0,
	}
	if lastMS > 0 {
		s.LastNotifiedAt = time.UnixMilli(lastMS)
	}
	return s, nil
}

func (r *incidentSubStateRepo) Upsert(ctx context.Context, s *subscriber.IncidentSubState) error {
	var lastMS int64
	if !s.LastNotifiedAt.IsZero() {
		lastMS = s.LastNotifiedAt.UnixMilli()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO incident_sub_states (incident_id, subscription_id, last_notified_at, notify_count, resolution_sent)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(incident_id, subscription_id) DO UPDATE SET
            last_notified_at=excluded.last_notified_at,
            notify_count=excluded.notify_count,
            resolution_sent=excluded.resolution_sent`,
		s.IncidentID, s.SubscriptionID, lastMS, s.NotifyCount, boolInt(s.ResolutionSent))
	return err
}

func (r *incidentSubStateRepo) ListForIncident(ctx context.Context, incidentID string) ([]*subscriber.IncidentSubState, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT incident_id, subscription_id, last_notified_at, notify_count, resolution_sent FROM incident_sub_states WHERE incident_id = ?`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*subscriber.IncidentSubState
	for rows.Next() {
		var (
			incID, subID    string
			lastMS          int64
			count, resolved int
		)
		if err := rows.Scan(&incID, &subID, &lastMS, &count, &resolved); err != nil {
			return nil, err
		}
		s := &subscriber.IncidentSubState{IncidentID: incID, SubscriptionID: subID, NotifyCount: count, ResolutionSent: resolved != 0}
		if lastMS > 0 {
			s.LastNotifiedAt = time.UnixMilli(lastMS)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
