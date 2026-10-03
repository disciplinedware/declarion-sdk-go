package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/errs"
	"github.com/stretchr/testify/require"
)

func TestErrorTypeUsesOnlyDeclaredCodesAndTransportFacts(t *testing.T) {
	saved := errs.ProcessRenderContext("en", "", 0)
	errs.SetCatalogue(errs.Catalogue{"test.refused": &errs.TypeDef{Status: 403}}, "en")
	t.Cleanup(func() { errs.SetCatalogue(saved.Catalogue, saved.DefaultLocale) })
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"declared", fmt.Errorf("outer: %w", &errs.Error{Type: "test.refused"}), "test.refused"},
		{"undeclared", &errs.Error{Type: "test.private_secret"}, "_OTHER"},
		{"oversized", &errs.Error{Type: "test." + strings.Repeat("private_secret", 1024)}, "_OTHER"},
		{"generic", errors.New("private_secret"), "_OTHER"},
		{"canceled", fmt.Errorf("outer: %w", context.Canceled), "canceled"},
		{"deadline", fmt.Errorf("outer: %w", context.DeadlineExceeded), "timeout"},
		{"network", &url.Error{Op: "Get", URL: "https://private_secret", Err: errors.New("connection refused")}, "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ErrorType(tc.err))
		})
	}
}
