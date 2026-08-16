package errscheck_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/disciplinedware/declarion-sdk-go/errs"
	"github.com/disciplinedware/declarion-sdk-go/errs/errscheck"
)

func fixtureCatalogue() errs.Catalogue {
	return errs.Catalogue{
		"entity.stale_object": {
			Status: 409,
			Title:  errs.LocalizedString{"en": "The record changed since you loaded it."},
			Fields: map[string]string{"row_version": "integer", "stored_row_version": "integer"},
		},
		"llm-connector.upstream_error": {
			Status: 502,
			Title:  errs.LocalizedString{"en": "The provider refused the request."},
			Fields: map[string]string{"upstream_status": "integer"},
		},
	}
}

// testdata/good also carries a _test.go file whose call site would fail the
// gate, so this passing is both halves: the shipped file is clean AND the
// test-file exclusion is scoped to test files rather than swallowing the
// directory.
func TestCheckPassesAWellWrittenPackage(t *testing.T) {
	findings, err := errscheck.Check("testdata/good", fixtureCatalogue(), errscheck.Options{})
	require.NoError(t, err)
	assert.Empty(t, findings, "findings: %v", findings)
}

// Each row is an injected regression: the gate is not a gate until it has
// been seen to fail on the exact fault it exists to catch.
func TestCheckCatchesEveryFault(t *testing.T) {
	findings, err := errscheck.Check("testdata/bad", fixtureCatalogue(), errscheck.Options{})
	require.NoError(t, err)

	joined := make([]string, len(findings))
	for i, f := range findings {
		joined[i] = f.String()
	}
	all := strings.Join(joined, "\n")

	tests := []struct {
		name string
		want string
	}{
		{name: "undeclared_code", want: "entity.stale_objekt: no module declares this type"},
		{name: "old_upper_snake_spelling", want: "STALE_OBJECT: not spelled <owner>.<name> in lower_snake"},
		{name: "undeclared_member", want: `member "rowversion" is not declared by this type`},
		{name: "illegal_member_name", want: `member "id" is not a legal name`},
		{name: "two_args", want: "at most one Args is legal"},
		{name: "code_assembled_at_runtime", want: "the code must be a string literal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, all, tt.want)
		})
	}
	assert.Len(t, findings, len(tests), "one finding per fault, no more:\n%s", all)
}

func TestCheckRefusesAnEmptyCatalogue(t *testing.T) {
	_, err := errscheck.Check("testdata/good", nil, errscheck.Options{})
	require.Error(t, err, "a gate that reads nothing must not report ok")
	assert.Contains(t, err.Error(), "empty catalogue")
}

func TestCheckHonoursExemptFields(t *testing.T) {
	findings, err := errscheck.Check("testdata/bad", fixtureCatalogue(), errscheck.Options{
		ExemptFields: []string{"rowversion"},
	})
	require.NoError(t, err)
	for _, f := range findings {
		assert.NotContains(t, f.Message, "rowversion")
	}
}
