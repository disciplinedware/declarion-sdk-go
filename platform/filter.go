package platform

// FilterNode is the client-side mirror of the server's filter tree
// (declarion-core store.FilterNode). Serialized as a JSON array in the
// `filters` query param on List requests.
//
// A node is either:
//   - A field condition (Field + Op + optional Value).
//   - A logical OR group (Or: direct child nodes are OR-ed).
//   - An explicit AND group (And: direct child nodes are AND-ed).
//
// The SERVER owns the grammar and enforces it: an operator allowlist, a
// nesting cap, and the rules for empty and mixed nodes. The SDK does NOT
// validate client-side, deliberately - a second validator is a second opinion,
// and the server's is the only one that decides. An illegal tree returns 422
// naming what is wrong.
//
// A node is EITHER a field condition OR a group, never both and never empty.
// The live vocabulary and the cap are published on the schema payload at
// GET /api/schema under `filter_grammar` (`operators`, `no_value_ops`,
// `multi_value_ops`); read it there rather than trusting the constants below
// to be current.
type FilterNode struct {
	// Field condition - set Field + Op together. Examples of Field:
	//   "score", "company_id",
	//   "$status.pipeline"  (status-group filter),
	//   "$property.industry" (JSONB property filter).
	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	// Value is any of: string, number, bool, []any (for in/not_in/between),
	// or nil for no-value operators (is_empty, is_not_empty, relative dates).
	//
	// It may also be a VALUE TOKEN, resolved server-side per request:
	//   "$user.id" / "$user.email" / "$user.tenant_id" - the calling user.
	//   "$now", with composable signed offsets - "$now+3w", "$now+3w-1d".
	//     Units: ns, us, ms, s, m, h, d (24h), w (7d). Calendar units y and mo
	//     are refused. A value that begins with "$now" must be a complete
	//     expression; a typo returns 422 rather than being compared as a string.
	// Every other `$`-prefixed value is an ordinary literal.
	Value any `json:"value,omitempty"`

	// Logical grouping (recursive).
	// Or: direct child nodes are OR-ed.
	Or []FilterNode `json:"or,omitempty"`
	// And: explicit AND (rarely needed — top-level slice is implicit AND).
	And []FilterNode `json:"and,omitempty"`
}

// Filter operator constants.
//
// The AUTHORITY is the server's one vocabulary (declarion-core
// engine.AllFilterOperators), published at GET /api/schema under
// `filter_grammar.operators`. These constants are a convenience for writing
// Go, not a second definition: Go's internal/ visibility rule means this
// package cannot import the server's declaration, so the copy is unavoidable -
// what is avoidable is trusting it silently. A client that must know the live
// set reads the schema payload.
const (
	OpEq         = "eq"
	OpNotEq      = "not_eq"
	OpGt         = "gt"
	OpGte        = "gte"
	OpLt         = "lt"
	OpLte        = "lte"
	OpIn         = "in"
	OpNotIn      = "not_in"
	OpBetween    = "between"
	OpContains   = "contains"
	OpStartsWith = "starts_with"
	// OpIsEmpty matches NULL or zero-length string / empty array. Use this
	// for "IS NULL" queries; there is no distinct is_null operator.
	OpIsEmpty    = "is_empty"
	OpIsNotEmpty = "is_not_empty"

	// Relative date operators. Apply to date / timestamp fields. No value.
	OpToday       = "today"
	OpThisWeek    = "this_week"
	OpThisMonth   = "this_month"
	OpLast7Days   = "last_7_days"
	OpLast30Days  = "last_30_days"
	OpLastHour    = "last_hour"
	OpLast24Hours = "last_24_hours"
)

// Eq is a convenience constructor for a field equality node.
func Eq(field string, value any) FilterNode {
	return FilterNode{Field: field, Op: OpEq, Value: value}
}

// IsEmpty is a convenience constructor for "IS NULL or empty" on a field.
func IsEmpty(field string) FilterNode {
	return FilterNode{Field: field, Op: OpIsEmpty}
}

// IsNotEmpty is a convenience constructor for "IS NOT NULL and not empty".
func IsNotEmpty(field string) FilterNode {
	return FilterNode{Field: field, Op: OpIsNotEmpty}
}

// In is a convenience constructor for the "in" operator.
func In(field string, values ...any) FilterNode {
	return FilterNode{Field: field, Op: OpIn, Value: values}
}

// Or constructs a logical OR node from direct child nodes.
func Or(nodes ...FilterNode) FilterNode {
	return FilterNode{Or: nodes}
}

// And constructs a logical AND node from direct child nodes.
func And(nodes ...FilterNode) FilterNode {
	return FilterNode{And: nodes}
}
