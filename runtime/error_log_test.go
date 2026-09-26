package runtime

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

// A cause never crosses the wire, so the sidecar that drops it is the one place
// it can be logged. Each case asserts both halves: what the operator reads, and
// that the caller's response does not carry it.
func TestAFailureCauseIsLoggedWhereItIsDropped(t *testing.T) {
	const secretCause = "provider answered status=incomplete reason=content_filter"
	cases := []struct {
		name       string
		handlerErr error
		wantLogged bool
		wantLevel  zapcore.Level
		wantType   string
	}{
		{
			name:       "typed_error_with_cause_logs_at_warn",
			handlerErr: errs.New("platform.external_service_error").Because(errors.New(secretCause)),
			wantLogged: true, wantLevel: zapcore.WarnLevel, wantType: "platform.external_service_error",
		},
		{
			name:       "typed_error_wrapped_on_the_way_out_logs_the_wrapping_too",
			handlerErr: fmt.Errorf("classify lead: %w", errs.New("platform.external_service_error").Because(errors.New(secretCause))),
			wantLogged: true, wantLevel: zapcore.WarnLevel, wantType: "platform.external_service_error",
		},
		{
			name:       "untyped_error_is_a_defect_and_logs_at_error",
			handlerErr: errors.New(secretCause),
			wantLogged: true, wantLevel: zapcore.ErrorLevel, wantType: errs.CodeInternalError,
		},
		{
			name:       "typed_error_without_cause_is_not_logged_twice",
			handlerErr: errs.New("platform.external_service_error"),
			wantLogged: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ClearHandlerRegistry()
			RegisterHandler[echoParams, echoResult]("test.fail", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
				return echoResult{}, tc.handlerErr
			})
			srv, logs := setupTestServerObservingLogs(t)
			defer srv.Close()

			req, err := http.NewRequest("POST", srv.URL+"/rpc",
				strings.NewReader(`{"jsonrpc":"2.0","id":"req-1","method":"test.fail","params":{"name":"x"}}`))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+mintTestToken(t, "tenant-1", "user-1", "test.fail", "audit-op-1"))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.NotContains(t, string(body), "content_filter", "a cause never travels")
			entries := logs.FilterMessage("request failed").All()
			if !tc.wantLogged {
				assert.Empty(t, entries)
				return
			}
			require.Len(t, entries, 1, "logged exactly once")
			got := entries[0]
			assert.Equal(t, tc.wantLevel, got.Level)
			fields := got.ContextMap()
			assert.Equal(t, tc.wantType, fields["type"])
			assert.Contains(t, fmt.Sprint(fields["error"]), secretCause)
			assert.Equal(t, "test.fail", fields["method"])
			assert.Equal(t, "tenant-1", fields["tenant_id"])
			assert.Equal(t, "audit-op-1", fields["audit_op"], "the line joins the audit record it belongs to")
			if strings.Contains(tc.name, "wrapped") {
				assert.Contains(t, fmt.Sprint(fields["error"]), "classify lead")
			}
		})
	}
}
