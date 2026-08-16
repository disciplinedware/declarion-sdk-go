package errscheck

import (
	"strings"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

// AssertNoUndeclaredTypes fails the test when any errs.New call site under root
// names a code the catalogue does not declare, misspells one, passes more than
// one Args, or passes a member the type does not declare.
//
// An application calls this ONCE, against the catalogue its own loader produced
// in the same test process - so the gate reads the declarations that will
// actually ship rather than a fixture written beside it. An empty catalogue is
// a failure, never a skip: a gate reporting ok having read nothing is the
// cheapest possible lie.
//
// One blind spot, stated because a reader must not mistake silence for
// coverage: a call site whose code is assembled at runtime rather than written
// as a literal cannot be read from the source, and is not reported. A generic
// writer that forwards a code it was handed is the legitimate shape of that,
// and the code it forwards was itself checked wherever it was written down.
func AssertNoUndeclaredTypes(t *testing.T, root string, cat errs.Catalogue, opts Options) {
	t.Helper()
	findings, err := Check(root, cat, opts)
	if err != nil {
		t.Fatalf("errscheck: %v", err)
	}
	if len(findings) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("errs.New call sites the catalogue does not support:\n")
	for _, f := range findings {
		b.WriteString("  " + f.String() + "\n")
	}
	t.Fatal(b.String())
}
