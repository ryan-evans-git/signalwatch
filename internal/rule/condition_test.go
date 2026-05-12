package rule

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ----- Op -----

func TestOpCompare(t *testing.T) {
	cases := []struct {
		op   Op
		a, b float64
		want bool
	}{
		{OpGT, 11, 10, true}, {OpGT, 10, 10, false},
		{OpGTE, 10, 10, true}, {OpGTE, 9, 10, false},
		{OpLT, 9, 10, true}, {OpLT, 10, 10, false},
		{OpLTE, 10, 10, true}, {OpLTE, 11, 10, false},
		{OpEQ, 10, 10, true}, {OpEQ, 10, 11, false},
		{OpNEQ, 10, 11, true}, {OpNEQ, 10, 10, false},
		{Op("bogus"), 1, 1, false}, // unknown op falls through to false
	}
	for _, tc := range cases {
		t.Run(string(tc.op)+"/"+tcname(tc.a, tc.b), func(t *testing.T) {
			if got := tc.op.Compare(tc.a, tc.b); got != tc.want {
				t.Fatalf("Compare(%v %s %v): want %v got %v", tc.a, tc.op, tc.b, tc.want, got)
			}
		})
	}
}

func tcname(a, b float64) string {
	return strings.ReplaceAll(strings.ReplaceAll(
		"a="+ftoa(a)+",b="+ftoa(b), " ", "_"), ".", "p")
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestOpValidate(t *testing.T) {
	for _, op := range []Op{OpGT, OpGTE, OpLT, OpLTE, OpEQ, OpNEQ} {
		if err := op.Validate(); err != nil {
			t.Fatalf("op %s: want nil, got %v", op, err)
		}
	}
	if err := Op("nope").Validate(); err == nil {
		t.Fatalf("invalid op: want error")
	}
}

// ----- Threshold -----

func TestThreshold_Mode(t *testing.T) {
	if got := (Threshold{}).Mode(); got != ModePush {
		t.Fatalf("Threshold.Mode: want push, got %s", got)
	}
}

func TestThreshold_Compile_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      Threshold
		wantSub string
	}{
		{"empty field", Threshold{Field: "", Op: OpGT, Value: 1}, "field required"},
		{"invalid op", Threshold{Field: "x", Op: Op("nope"), Value: 1}, "invalid op"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.Compile(nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestThreshold_Eval(t *testing.T) {
	c, err := Threshold{Field: "cpu", Op: OpGT, Value: 90}.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	t.Run("nil record errors", func(t *testing.T) {
		_, _, err := c.Eval(EvalContext{}, nil)
		if err == nil {
			t.Fatalf("want error")
		}
	})

	t.Run("missing field is not-triggered, no error", func(t *testing.T) {
		triggered, _, err := c.Eval(EvalContext{}, Record{"other": 1.0})
		if err != nil || triggered {
			t.Fatalf("want (false, nil), got (%v, %v)", triggered, err)
		}
	})

	t.Run("non-numeric value is not-triggered, no error", func(t *testing.T) {
		triggered, _, err := c.Eval(EvalContext{}, Record{"cpu": "high"})
		if err != nil || triggered {
			t.Fatalf("want (false, nil), got (%v, %v)", triggered, err)
		}
	})

	// Numeric coercion: each accepted type compares against Value=90.
	// json.Number("not-a-number") fails Float64() and falls through to
	// the (0, false) early return — triggered stays false with an empty
	// value summary, mirroring the missing-field path.
	numericCases := []struct {
		name     string
		val      any
		want     bool
		emptyVal bool // expect empty value summary (the (0,false) path)
	}{
		{"float64 over", float64(95), true, false},
		{"float64 under", float64(50), false, false},
		{"float32 over", float32(95.5), true, false},
		{"int over", int(91), true, false},
		{"int32 under", int32(50), false, false},
		{"int64 over", int64(100), true, false},
		{"json.Number over", json.Number("99"), true, false},
		{"json.Number malformed", json.Number("not-a-number"), false, true},
	}
	for _, tc := range numericCases {
		t.Run(tc.name, func(t *testing.T) {
			triggered, value, err := c.Eval(EvalContext{}, Record{"cpu": tc.val})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if triggered != tc.want {
				t.Fatalf("triggered: want %v got %v", tc.want, triggered)
			}
			switch {
			case tc.emptyVal && value != "":
				t.Fatalf("malformed numeric: want empty value, got %q", value)
			case !tc.emptyVal && value == "":
				t.Fatalf("value summary should be non-empty")
			}
		})
	}
}

func TestThreshold_MarshalCondition(t *testing.T) {
	got, err := Threshold{Field: "cpu", Op: OpGT, Value: 90}.MarshalCondition()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var w struct {
		Type string         `json:"type"`
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal(got, &w); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if w.Type != "threshold" {
		t.Fatalf("type: want threshold, got %q", w.Type)
	}
	if w.Spec["field"] != "cpu" {
		t.Fatalf("spec.field: want cpu, got %v", w.Spec["field"])
	}
}

// ----- WindowAggregate -----

// stubHelpers satisfies ExprHelpers for testing without bringing in the
// real WindowBuffers. Each method returns the canned value plus the ok flag.
// RecordRegexMatch is a no-op because compiled conditions don't exercise it.
type stubHelpers struct {
	avg, sum, min, max  float64
	count               int
	avgOK, sumOK, minOK bool
	maxOK, countOK      bool
}

func (s *stubHelpers) AvgOver(_ string, _ time.Duration) (float64, bool) {
	return s.avg, s.avgOK
}
func (s *stubHelpers) SumOver(_ string, _ time.Duration) (float64, bool) {
	return s.sum, s.sumOK
}
func (s *stubHelpers) MinOver(_ string, _ time.Duration) (float64, bool) {
	return s.min, s.minOK
}
func (s *stubHelpers) MaxOver(_ string, _ time.Duration) (float64, bool) {
	return s.max, s.maxOK
}
func (s *stubHelpers) CountOver(_ string, _ time.Duration) (int, bool) {
	return s.count, s.countOK
}
func (s *stubHelpers) RecordRegexMatch(_, _ string) (bool, error) {
	return false, nil
}

func TestWindowAggregate_Mode(t *testing.T) {
	if got := (WindowAggregate{}).Mode(); got != ModeScheduled {
		t.Fatalf("WindowAggregate.Mode: want scheduled, got %s", got)
	}
}

func TestWindowAggregate_Compile_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      WindowAggregate
		wantSub string
	}{
		{"empty field", WindowAggregate{Agg: AggAvg, Window: time.Minute, Op: OpGT}, "field required"},
		{"zero window", WindowAggregate{Field: "v", Agg: AggAvg, Window: 0, Op: OpGT}, "window must be > 0"},
		{"negative window", WindowAggregate{Field: "v", Agg: AggAvg, Window: -1, Op: OpGT}, "window must be > 0"},
		{"invalid agg", WindowAggregate{Field: "v", Agg: "median", Window: time.Minute, Op: OpGT}, "invalid agg"},
		{"invalid op", WindowAggregate{Field: "v", Agg: AggAvg, Window: time.Minute, Op: Op("?")}, "invalid op"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.Compile(nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestWindowAggregate_Compile_ValidPerAgg(t *testing.T) {
	for _, agg := range []Agg{AggAvg, AggSum, AggMin, AggMax, AggCount} {
		t.Run(string(agg), func(t *testing.T) {
			c, err := WindowAggregate{
				Field: "v", Agg: agg, Window: time.Minute, Op: OpLT, Value: 10,
			}.Compile(nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if c == nil {
				t.Fatal("nil compiled")
			}
		})
	}
}

func TestWindowAggregate_Eval_NoHelpersErrors(t *testing.T) {
	c, err := WindowAggregate{
		Field: "v", Agg: AggAvg, Window: time.Minute, Op: OpLT, Value: 10,
	}.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, _, err = c.Eval(EvalContext{Helpers: nil}, nil)
	if err == nil {
		t.Fatalf("want error for nil helpers")
	}
}

func TestWindowAggregate_Eval_PerAgg(t *testing.T) {
	// Each helper returns 5, comparing < 10 → triggered.
	cases := []struct {
		name string
		agg  Agg
		ok   func(*stubHelpers)
	}{
		{"avg", AggAvg, func(s *stubHelpers) { s.avg = 5; s.avgOK = true }},
		{"sum", AggSum, func(s *stubHelpers) { s.sum = 5; s.sumOK = true }},
		{"min", AggMin, func(s *stubHelpers) { s.min = 5; s.minOK = true }},
		{"max", AggMax, func(s *stubHelpers) { s.max = 5; s.maxOK = true }},
		{"count", AggCount, func(s *stubHelpers) { s.count = 5; s.countOK = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_triggered", func(t *testing.T) {
			h := &stubHelpers{}
			tc.ok(h)
			c, err := WindowAggregate{
				Field: "v", Agg: tc.agg, Window: time.Minute, Op: OpLT, Value: 10,
			}.Compile(nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			triggered, value, err := c.Eval(EvalContext{Helpers: h}, nil)
			if err != nil {
				t.Fatalf("eval err: %v", err)
			}
			if !triggered {
				t.Fatalf("want triggered=true, got false")
			}
			if !strings.Contains(value, string(tc.agg)) {
				t.Fatalf("value %q missing %q", value, tc.agg)
			}
		})

		t.Run(tc.name+"_no_data", func(t *testing.T) {
			// All-zero stub means *OK flags are false; expect (false, "", nil)
			c, _ := WindowAggregate{
				Field: "v", Agg: tc.agg, Window: time.Minute, Op: OpLT, Value: 10,
			}.Compile(nil)
			triggered, value, err := c.Eval(EvalContext{Helpers: &stubHelpers{}}, nil)
			if err != nil || triggered || value != "" {
				t.Fatalf("want (false, \"\", nil), got (%v, %q, %v)", triggered, value, err)
			}
		})
	}
}

func TestWindowAggregate_MarshalCondition(t *testing.T) {
	got, err := WindowAggregate{Field: "v", Agg: AggAvg, Window: time.Minute, Op: OpLT, Value: 5}.MarshalCondition()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(got), `"type":"window_aggregate"`) {
		t.Fatalf("missing type discriminator: %s", got)
	}
}

// ----- PatternMatch -----

func TestPatternMatch_Mode(t *testing.T) {
	if got := (PatternMatch{}).Mode(); got != ModePush {
		t.Fatalf("PatternMatch.Mode: want push, got %s", got)
	}
}

func TestPatternMatch_Compile_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      PatternMatch
		wantSub string
	}{
		{"empty field", PatternMatch{Kind: MatchContains, Pattern: "x"}, "field required"},
		{"invalid kind", PatternMatch{Field: "f", Kind: "exact", Pattern: "x"}, "invalid kind"},
		{"regex parse error", PatternMatch{Field: "f", Kind: MatchRegex, Pattern: "[invalid"}, "pattern_match"},
		{"contains empty pattern", PatternMatch{Field: "f", Kind: MatchContains, Pattern: ""}, "pattern required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.Compile(nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestPatternMatch_Eval_Regex(t *testing.T) {
	c, err := PatternMatch{Field: "msg", Kind: MatchRegex, Pattern: "ERR.+timeout"}.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, _, err := c.Eval(EvalContext{}, nil); err == nil {
		t.Fatalf("nil record: want error")
	}

	triggered, _, err := c.Eval(EvalContext{}, Record{"msg": "ERR: read timeout"})
	if err != nil || !triggered {
		t.Fatalf("want triggered, got (%v, %v)", triggered, err)
	}

	triggered, _, err = c.Eval(EvalContext{}, Record{"msg": "INFO ok"})
	if err != nil || triggered {
		t.Fatalf("want not-triggered, got (%v, %v)", triggered, err)
	}

	triggered, _, err = c.Eval(EvalContext{}, Record{"other": "x"})
	if err != nil || triggered {
		t.Fatalf("missing field should be not-triggered, no error; got (%v, %v)", triggered, err)
	}
}

func TestPatternMatch_Eval_Contains(t *testing.T) {
	c, err := PatternMatch{Field: "msg", Kind: MatchContains, Pattern: "panic"}.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	triggered, _, err := c.Eval(EvalContext{}, Record{"msg": "system panic detected"})
	if err != nil || !triggered {
		t.Fatalf("want triggered, got (%v, %v)", triggered, err)
	}
	triggered, _, err = c.Eval(EvalContext{}, Record{"msg": "all good"})
	if err != nil || triggered {
		t.Fatalf("want not-triggered, got (%v, %v)", triggered, err)
	}
}

func TestPatternMatch_Eval_NonStringField(t *testing.T) {
	// stringField falls back to fmt.Sprintf("%v", v) for non-string types,
	// so a numeric value still gets matched against the pattern.
	c, _ := PatternMatch{Field: "n", Kind: MatchContains, Pattern: "42"}.Compile(nil)
	triggered, _, err := c.Eval(EvalContext{}, Record{"n": 42})
	if err != nil || !triggered {
		t.Fatalf("non-string field should still stringify; got (%v, %v)", triggered, err)
	}
}

func TestPatternMatch_MarshalCondition(t *testing.T) {
	got, err := PatternMatch{Field: "msg", Kind: MatchContains, Pattern: "x"}.MarshalCondition()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(got), `"type":"pattern_match"`) {
		t.Fatalf("missing type discriminator: %s", got)
	}
}

// ----- SQLReturnsRows -----

func TestSQLReturnsRows_Mode(t *testing.T) {
	if got := (SQLReturnsRows{}).Mode(); got != ModeScheduled {
		t.Fatalf("SQL.Mode: want scheduled, got %s", got)
	}
}

func TestSQLReturnsRows_Compile_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      SQLReturnsRows
		wantSub string
	}{
		{"missing datasource", SQLReturnsRows{Query: "select 1"}, "data_source required"},
		{"empty query", SQLReturnsRows{DataSource: "main", Query: ""}, "query required"},
		{"whitespace query", SQLReturnsRows{DataSource: "main", Query: "   \n  "}, "query required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.Compile(nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestSQLReturnsRows_Compile_DefaultsMinRows(t *testing.T) {
	c, err := SQLReturnsRows{DataSource: "main", Query: "select 1"}.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	spec, ok := c.(SQLSpec)
	if !ok {
		t.Fatalf("compiled value does not implement SQLSpec")
	}
	ds, q, min := spec.Spec()
	if ds != "main" || q != "select 1" || min != 1 {
		t.Fatalf("Spec: got (%q, %q, %d), want (main, select 1, 1)", ds, q, min)
	}
}

func TestSQLReturnsRows_Compile_PreservesPositiveMinRows(t *testing.T) {
	c, _ := SQLReturnsRows{DataSource: "main", Query: "q", MinRows: 5}.Compile(nil)
	_, _, min := c.(SQLSpec).Spec()
	if min != 5 {
		t.Fatalf("want min=5, got %d", min)
	}
}

func TestSQLReturnsRows_Eval_NotDirectlyEvaluable(t *testing.T) {
	c, _ := SQLReturnsRows{DataSource: "main", Query: "q"}.Compile(nil)
	_, _, err := c.Eval(EvalContext{}, nil)
	if err == nil {
		t.Fatalf("want error: SQL must be dispatched by the scheduled evaluator")
	}
}

func TestSQLReturnsRows_MarshalCondition(t *testing.T) {
	got, err := SQLReturnsRows{DataSource: "main", Query: "select 1", MinRows: 2}.MarshalCondition()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(got), `"type":"sql_returns_rows"`) {
		t.Fatalf("missing type discriminator: %s", got)
	}
}

// ----- UnmarshalCondition -----

func TestUnmarshalCondition_RoundTrip(t *testing.T) {
	originals := []Condition{
		Threshold{Field: "x", Op: OpGT, Value: 1},
		WindowAggregate{Field: "x", Agg: AggAvg, Window: time.Second, Op: OpLT, Value: 5},
		PatternMatch{Field: "x", Kind: MatchContains, Pattern: "y"},
		SQLReturnsRows{DataSource: "d", Query: "q", MinRows: 1},
	}
	for _, orig := range originals {
		raw, err := orig.MarshalCondition()
		if err != nil {
			t.Fatalf("marshal %T: %v", orig, err)
		}
		got, err := UnmarshalCondition(raw)
		if err != nil {
			t.Fatalf("unmarshal %T: %v", orig, err)
		}
		if got.Mode() != orig.Mode() {
			t.Fatalf("%T: mode lost (got %s want %s)", orig, got.Mode(), orig.Mode())
		}
	}
}

func TestUnmarshalCondition_Errors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{"malformed JSON", "{bogus", "condition:"},
		{"unknown type", `{"type":"weather","spec":{}}`, "unknown type"},
		{"bad threshold spec", `{"type":"threshold","spec":"not-an-object"}`, ""},
		{"bad window_aggregate spec", `{"type":"window_aggregate","spec":42}`, ""},
		{"bad pattern_match spec", `{"type":"pattern_match","spec":true}`, ""},
		{"bad sql_returns_rows spec", `{"type":"sql_returns_rows","spec":[1,2]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnmarshalCondition([]byte(tc.raw))
			if err == nil {
				t.Fatalf("want error")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// ----- numericField / stringField -----

func TestStringField(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if v, ok := stringField(Record{}, "x"); ok || v != "" {
			t.Fatalf("missing field: want (\"\", false), got (%q, %v)", v, ok)
		}
	})
	t.Run("string", func(t *testing.T) {
		if v, ok := stringField(Record{"x": "hello"}, "x"); !ok || v != "hello" {
			t.Fatalf("string: got (%q, %v)", v, ok)
		}
	})
	t.Run("int stringifies", func(t *testing.T) {
		if v, ok := stringField(Record{"x": 42}, "x"); !ok || v != "42" {
			t.Fatalf("int: got (%q, %v)", v, ok)
		}
	})
}
