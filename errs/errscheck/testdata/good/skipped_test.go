package good

import "github.com/disciplinedware/declarion-sdk-go/errs"

// A test proving the undeclared-type fallback must be able to raise one, so
// the gate skips test files. This call site would fail it otherwise.
func undeclaredOnPurpose() error { return errs.New("nobody.declares_this") }
