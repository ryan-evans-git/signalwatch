package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Condition is the user-facing condition definition. Implementations are
// compiled into a Compiled by Compile() before evaluation.
type Condition interface {
	Mode() Mode
	// CompileWith compiles this condition against the given expression
	// helpers. Implementations that don't need helpers can ignore them.
	Compile(helpers ExprHelpers) (Compiled, error)
	// MarshalCondition emits the condition with a "type" discriminator.
	MarshalCondition() ([]byte, error)
}

// Compiled is the runtime form of a condition. Evaluator calls Eval with
// either a single record (push mode) or an EvalContext (scheduled mode).
type Compiled interface {
	// Eval runs the compiled condition. record may be nil for scheduled rules.
	Eval(ctx EvalContext, record Record) (triggered bool, value string, err error)
}

// Record is a single input record presented to a rule. Field types are
// limited to JSON-compatible scalars so storage and expression evaluation
// stay simple.
type Record map[string]any

// EvalContext gives a Compiled access to time-based helpers. The
// expression-based conditions use ExprHelpers; threshold/pattern_match do not.
type EvalContext struct {
	Now     time.Time
	Helpers ExprHelpers
}

// ExprHelpers exposes window-aware helper functions to the expression engine
// (avg_over, count_over, regex_match). Push-evaluated rules typically pass a
// no-op implementation.
type ExprHelpers interface {
	AvgOver(field string, window time.Duration) (float64, bool)
	CountOver(field string, window time.Duration) (int, bool)
	MinOver(field string, window time.Duration) (float64, bool)
	MaxOver(field string, window time.Duration) (float64, bool)
	SumOver(field string, window time.Duration) (float64, bool)
	RecordRegexMatch(field, pattern string) (bool, error)
}

// Op is a comparison operator used by threshold and window_aggregate
// conditions.
type Op string

const (
	OpGT  Op = ">"
	OpGTE Op = ">="
	OpLT  Op = "<"
	OpLTE Op = "<="
	OpEQ  Op = "=="
	OpNEQ Op = "!="
)

func (o Op) Compare(a, b float64) bool {
	switch o {
	case OpGT:
		return a > b
	case OpGTE:
		return a >= b
	case OpLT:
		return a < b
	case OpLTE:
		return a <= b
	case OpEQ:
		return a == b
	case OpNEQ:
		return a != b
	}
	return false
}

func (o Op) Validate() error {
	switch o {
	case OpGT, OpGTE, OpLT, OpLTE, OpEQ, OpNEQ:
		return nil
	}
	return fmt.Errorf("invalid op %q", o)
}

// ----- Threshold -----

// Threshold compares a numeric field on the record against a constant.
type Threshold struct {
	Field string  `json:"field"`
	Op    Op      `json:"op"`
	Value float64 `json:"value"`
}

func (Threshold) Mode() Mode { return ModePush }

func (t Threshold) Compile(_ ExprHelpers) (Compiled, error) {
	if t.Field == "" {
		return nil, fmt.Errorf("threshold: field required")
	}
	if err := t.Op.Validate(); err != nil {
		return nil, err
	}
	return &compiledThreshold{t: t}, nil
}

func (t Threshold) MarshalCondition() ([]byte, error) {
	return marshalCondition("threshold", t)
}

type compiledThreshold struct{ t Threshold }

func (c *compiledThreshold) Eval(_ EvalContext, r Record) (bool, string, error) {
	if r == nil {
		return false, "", fmt.Errorf("threshold: nil record")
	}
	v, ok := numericField(r, c.t.Field)
	if !ok {
		return false, "", nil
	}
	triggered := c.t.Op.Compare(v, c.t.Value)
	return triggered, fmt.Sprintf("%v %s %v", v, c.t.Op, c.t.Value), nil
}

// ----- WindowAggregate -----

// Agg is the aggregation used by WindowAggregate.
type Agg string

const (
	AggAvg   Agg = "avg"
	AggSum   Agg = "sum"
	AggMin   Agg = "min"
	AggMax   Agg = "max"
	AggCount Agg = "count"
)

// WindowAggregate aggregates a numeric field over a duration window and
// compares the result against a constant. Always scheduled.
type WindowAggregate struct {
	Field  string        `json:"field"`
	Agg    Agg           `json:"agg"`
	Window time.Duration `json:"window"`
	Op     Op            `json:"op"`
	Value  float64       `json:"value"`
}

func (WindowAggregate) Mode() Mode { return ModeScheduled }

func (w WindowAggregate) Compile(_ ExprHelpers) (Compiled, error) {
	if w.Field == "" {
		return nil, fmt.Errorf("window_aggregate: field required")
	}
	if w.Window <= 0 {
		return nil, fmt.Errorf("window_aggregate: window must be > 0")
	}
	switch w.Agg {
	case AggAvg, AggSum, AggMin, AggMax, AggCount:
	default:
		return nil, fmt.Errorf("window_aggregate: invalid agg %q", w.Agg)
	}
	if err := w.Op.Validate(); err != nil {
		return nil, err
	}
	return &compiledWindowAggregate{w: w}, nil
}

func (w WindowAggregate) MarshalCondition() ([]byte, error) {
	return marshalCondition("window_aggregate", w)
}

type compiledWindowAggregate struct{ w WindowAggregate }

func (c *compiledWindowAggregate) Eval(ctx EvalContext, _ Record) (bool, string, error) {
	if ctx.Helpers == nil {
		return false, "", fmt.Errorf("window_aggregate: no helpers")
	}
	var (
		v  float64
		ok bool
	)
	switch c.w.Agg {
	case AggAvg:
		v, ok = ctx.Helpers.AvgOver(c.w.Field, c.w.Window)
	case AggSum:
		v, ok = ctx.Helpers.SumOver(c.w.Field, c.w.Window)
	case AggMin:
		v, ok = ctx.Helpers.MinOver(c.w.Field, c.w.Window)
	case AggMax:
		v, ok = ctx.Helpers.MaxOver(c.w.Field, c.w.Window)
	case AggCount:
		var n int
		n, ok = ctx.Helpers.CountOver(c.w.Field, c.w.Window)
		v = float64(n)
	}
	if !ok {
		return false, "", nil
	}
	triggered := c.w.Op.Compare(v, c.w.Value)
	return triggered, fmt.Sprintf("%s(%s,%s)=%v %s %v", c.w.Agg, c.w.Field, c.w.Window, v, c.w.Op, c.w.Value), nil
}

// ----- PatternMatch -----

// MatchKind discriminates between regex and substring matching.
type MatchKind string

const (
	MatchRegex    MatchKind = "regex"
	MatchContains MatchKind = "contains"
)

// PatternMatch matches a string field on the record against a pattern.
type PatternMatch struct {
	Field   string    `json:"field"`
	Kind    MatchKind `json:"kind"`
	Pattern string    `json:"pattern"`
}

func (PatternMatch) Mode() Mode { return ModePush }

func (p PatternMatch) Compile(_ ExprHelpers) (Compiled, error) {
	if p.Field == "" {
		return nil, fmt.Errorf("pattern_match: field required")
	}
	switch p.Kind {
	case MatchRegex:
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern_match: %w", err)
		}
		return &compiledPattern{p: p, re: re}, nil
	case MatchContains:
		if p.Pattern == "" {
			return nil, fmt.Errorf("pattern_match: pattern required")
		}
		return &compiledPattern{p: p}, nil
	default:
		return nil, fmt.Errorf("pattern_match: invalid kind %q", p.Kind)
	}
}

func (p PatternMatch) MarshalCondition() ([]byte, error) {
	return marshalCondition("pattern_match", p)
}

type compiledPattern struct {
	p  PatternMatch
	re *regexp.Regexp
}

func (c *compiledPattern) Eval(_ EvalContext, r Record) (bool, string, error) {
	if r == nil {
		return false, "", fmt.Errorf("pattern_match: nil record")
	}
	s, ok := stringField(r, c.p.Field)
	if !ok {
		return false, "", nil
	}
	var triggered bool
	switch c.p.Kind {
	case MatchRegex:
		triggered = c.re.MatchString(s)
	case MatchContains:
		triggered = strings.Contains(s, c.p.Pattern)
	}
	return triggered, fmt.Sprintf("%s ~ %q -> %v", c.p.Field, c.p.Pattern, triggered), nil
}

// ----- SQLReturnsRows -----

// SQLReturnsRows runs a configured query against an external datasource
// (registered with the engine by name) on a schedule. Triggers when the
// query returns at least MinRows rows.
type SQLReturnsRows struct {
	DataSource string `json:"data_source"`
	Query      string `json:"query"`
	MinRows    int    `json:"min_rows"`
}

func (SQLReturnsRows) Mode() Mode { return ModeScheduled }

func (s SQLReturnsRows) Compile(_ ExprHelpers) (Compiled, error) {
	if s.DataSource == "" {
		return nil, fmt.Errorf("sql_returns_rows: data_source required")
	}
	if strings.TrimSpace(s.Query) == "" {
		return nil, fmt.Errorf("sql_returns_rows: query required")
	}
	min := s.MinRows
	if min <= 0 {
		min = 1
	}
	return &compiledSQL{s: s, minRows: min}, nil
}

func (s SQLReturnsRows) MarshalCondition() ([]byte, error) {
	return marshalCondition("sql_returns_rows", s)
}

// compiledSQL stashes the parameters; actual query execution is performed
// by the eval/scheduled package using a registry of named datasources. We
// keep this Compiled implementation as a marker plus a hook the evaluator
// can inspect.
type compiledSQL struct {
	s       SQLReturnsRows
	minRows int
}

func (c *compiledSQL) Spec() (datasource, query string, minRows int) {
	return c.s.DataSource, c.s.Query, c.minRows
}

func (c *compiledSQL) Eval(_ EvalContext, _ Record) (bool, string, error) {
	return false, "", fmt.Errorf("sql_returns_rows: not directly evaluable; the scheduled evaluator must dispatch via SQLEvaluator")
}

// SQLSpec exposes the underlying spec for the scheduled evaluator.
type SQLSpec interface {
	Spec() (datasource, query string, minRows int)
}

// ----- helpers -----

func marshalCondition(typ string, body any) ([]byte, error) {
	wrapper := map[string]any{
		"type": typ,
		"spec": body,
	}
	return json.Marshal(wrapper)
}

// UnmarshalCondition decodes the discriminated JSON form back into a typed
// Condition. Used by the store when loading rules.
func UnmarshalCondition(data []byte) (Condition, error) {
	var w struct {
		Type string          `json:"type"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("condition: %w", err)
	}
	switch w.Type {
	case "threshold":
		var t Threshold
		if err := json.Unmarshal(w.Spec, &t); err != nil {
			return nil, err
		}
		return t, nil
	case "window_aggregate":
		var a WindowAggregate
		if err := json.Unmarshal(w.Spec, &a); err != nil {
			return nil, err
		}
		return a, nil
	case "pattern_match":
		var p PatternMatch
		if err := json.Unmarshal(w.Spec, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "sql_returns_rows":
		var s SQLReturnsRows
		if err := json.Unmarshal(w.Spec, &s); err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, fmt.Errorf("condition: unknown type %q", w.Type)
	}
}

func numericField(r Record, field string) (float64, bool) {
	v, ok := r[field]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func stringField(r Record, field string) (string, bool) {
	v, ok := r[field]
	if !ok {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return fmt.Sprintf("%v", v), true
}
