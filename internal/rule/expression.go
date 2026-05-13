package rule

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// Expression is the v0.3 "escape hatch" condition. It evaluates an
// expr-lang program against either the inbound record (push mode) or
// the rule's helper environment (scheduled mode), and triggers when
// the program returns true.
//
// The same condition type covers both push and scheduled flavors via
// the EvalMode field. Push mode exposes `record` (a map keyed by field
// name) and the helper functions defined below; scheduled mode exposes
// only the helpers (record is nil).
//
// Helper functions available in either mode (delegated to ExprHelpers
// when present):
//
//	avg_over(field, window)   -> float64
//	sum_over(field, window)   -> float64
//	min_over(field, window)   -> float64
//	max_over(field, window)   -> float64
//	count_over(field, window) -> int
//	regex_match(field, pat)   -> bool
//
// `window` is a flex-duration string: anything time.ParseDuration
// accepts, plus "Xd" and "Xw" suffixes (e.g. "30d", "2w"). Examples:
//
//	avg_over("mpg", "30d") < 5
//	count_over("error_code", "5m") > 10
//	record.level == "ERROR" && regex_match("host", "^web-")
type Expression struct {
	Expr     string `json:"expr"`
	EvalMode Mode   `json:"mode,omitempty"` // "" or "push" → push; "scheduled" → scheduled
}

// Mode reports whether this Expression runs on every record (push) or
// on the rule schedule (scheduled). Defaults to push when the EvalMode
// field is empty.
func (e Expression) Mode() Mode {
	if e.EvalMode == ModeScheduled {
		return ModeScheduled
	}
	return ModePush
}

func (e Expression) MarshalCondition() ([]byte, error) {
	return marshalCondition("expression", e)
}

func (e Expression) Compile(_ ExprHelpers) (Compiled, error) {
	src := strings.TrimSpace(e.Expr)
	if src == "" {
		return nil, errors.New("expression: expr required")
	}
	switch e.EvalMode {
	case "", ModePush, ModeScheduled:
	default:
		return nil, fmt.Errorf("expression: invalid mode %q", e.EvalMode)
	}

	// Compile with helper signatures stubbed so the compiler can
	// type-check call sites. The actual helper implementations come
	// in via env at Run time.
	program, err := expr.Compile(src,
		expr.AsBool(),
		expr.Env(exprEnv{}),
	)
	if err != nil {
		return nil, fmt.Errorf("expression: %w", err)
	}
	return &compiledExpression{e: e, program: program}, nil
}

// exprEnv is the static type the compiler sees. Field names + helper
// signatures match what Eval populates at runtime. `Record` is wired
// to the `record` identifier in expr via the struct tag so rule
// authors can write `record.level` rather than `Record.level`.
type exprEnv struct {
	Record map[string]any `expr:"record"`

	AvgOver    func(field, window string) (float64, bool) `expr:"avg_over"`
	SumOver    func(field, window string) (float64, bool) `expr:"sum_over"`
	MinOver    func(field, window string) (float64, bool) `expr:"min_over"`
	MaxOver    func(field, window string) (float64, bool) `expr:"max_over"`
	CountOver  func(field, window string) (int, bool)     `expr:"count_over"`
	RegexMatch func(field, pattern string) (bool, error)  `expr:"regex_match"`
}

type compiledExpression struct {
	e       Expression
	program *vm.Program
}

func (c *compiledExpression) Eval(ctx EvalContext, r Record) (bool, string, error) {
	env := buildExprEnv(ctx, r)
	out, err := expr.Run(c.program, env)
	if err != nil {
		return false, "", fmt.Errorf("expression: %w", err)
	}
	b, ok := out.(bool)
	if !ok {
		return false, "", fmt.Errorf("expression: result %v is not bool", out)
	}
	return b, summarizeExpression(c.e.Expr, b), nil
}

// buildExprEnv constructs the runtime env, wiring helper funcs to
// ctx.Helpers when present. When Helpers is nil (push-only rule that
// doesn't use the window functions), every helper returns
// (zero, false) so the expression still type-checks at run time.
func buildExprEnv(ctx EvalContext, r Record) exprEnv {
	h := ctx.Helpers
	rec := map[string]any(r)
	if rec == nil {
		rec = map[string]any{}
	}
	return exprEnv{
		Record: rec,
		AvgOver: func(field, win string) (float64, bool) {
			if h == nil {
				return 0, false
			}
			d, ok := parseFlexDuration(win)
			if !ok {
				return 0, false
			}
			return h.AvgOver(field, d)
		},
		SumOver: func(field, win string) (float64, bool) {
			if h == nil {
				return 0, false
			}
			d, ok := parseFlexDuration(win)
			if !ok {
				return 0, false
			}
			return h.SumOver(field, d)
		},
		MinOver: func(field, win string) (float64, bool) {
			if h == nil {
				return 0, false
			}
			d, ok := parseFlexDuration(win)
			if !ok {
				return 0, false
			}
			return h.MinOver(field, d)
		},
		MaxOver: func(field, win string) (float64, bool) {
			if h == nil {
				return 0, false
			}
			d, ok := parseFlexDuration(win)
			if !ok {
				return 0, false
			}
			return h.MaxOver(field, d)
		},
		CountOver: func(field, win string) (int, bool) {
			if h == nil {
				return 0, false
			}
			d, ok := parseFlexDuration(win)
			if !ok {
				return 0, false
			}
			return h.CountOver(field, d)
		},
		RegexMatch: func(field, pattern string) (bool, error) {
			if h != nil {
				return h.RecordRegexMatch(field, pattern)
			}
			// Fall back to evaluating the regex against the current
			// record directly. Lets push-mode expressions use
			// regex_match without requiring helpers.
			if r == nil {
				return false, nil
			}
			s, ok := stringField(r, field)
			if !ok {
				return false, nil
			}
			return regexp.MatchString(pattern, s)
		},
	}
}

// parseFlexDuration accepts everything time.ParseDuration accepts plus
// "Xd" (days) and "Xw" (weeks) suffixes commonly used in alerting
// windows (e.g. "30d", "2w"). The suffix has to be at the very end of
// the string and not mixed with other units — i.e. "30d" is valid,
// "30d12h" is not (use "732h" instead).
func parseFlexDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n := len(s); n > 1 {
		last := s[n-1]
		if last == 'd' || last == 'w' {
			head := s[:n-1]
			x, err := strconv.ParseFloat(head, 64)
			if err != nil || x < 0 {
				return 0, false
			}
			unit := time.Hour * 24
			if last == 'w' {
				unit *= 7
			}
			return time.Duration(x * float64(unit)), true
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

// summarizeExpression returns a short human-readable LastValue string
// for the live-state UI. Truncates long source so the value column
// doesn't blow up.
func summarizeExpression(src string, triggered bool) string {
	const max = 80
	cleaned := strings.Join(strings.Fields(src), " ")
	if len(cleaned) > max {
		cleaned = cleaned[:max-1] + "…"
	}
	return fmt.Sprintf("%s -> %v", cleaned, triggered)
}
