package platform

// FilterNode is the client-side mirror of the server's filter tree
// (declarion-core store.FilterNode). Serialized as a JSON array in the
// `filters` query param on List requests.
//
// A node is either:
//   - A field condition (Field + Op + optional Value).
//   - A logical OR group (Or: each inner slice is AND-ed, groups are OR-ed).
//   - An explicit AND group (And: rarely needed, top-level is implicit AND).
//
// Server enforces an allowlist of operators (see the Op* constants below) and
// a max nesting depth of 4. Unknown operators return 422; the SDK does NOT
// validate client-side to avoid drift with server policy.
type FilterNode struct {
	// Field condition - set Field + Op together. Examples of Field:
	//   "score", "company_id",
	//   "$status.pipeline"  (status-group filter),
	//   "$property.industry" (JSONB property filter).
	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	// Value is any of: string, number, bool, []any (for in/not_in/between),
	// or nil for no-value operators (is_empty, is_not_empty, relative dates).
	Value any `json:"value,omitempty"`

	// Logical grouping (recursive).
	// Or: each inner slice is AND-ed internally, groups are OR-ed together.
	Or [][]FilterNode `json:"or,omitempty"`
	// And: explicit AND (rarely needed — top-level slice is implicit AND).
	And []FilterNode `json:"and,omitempty"`
}

// Filter operator constants. Mirrors the server's allowlist exactly; changes
// require a coordinated SDK + server release.
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
