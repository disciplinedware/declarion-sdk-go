package tracing

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTraceStateSyntaxIsValidatedWithoutLosingTheParent(t *testing.T) {
	var members []string
	for i := range 32 {
		members = append(members, fmt.Sprintf("vendor%d=value", i))
	}
	maximumMembers := strings.Join(members, ",")
	maximumValue := "vendor=" + strings.Repeat("x", 256)
	for _, tc := range []struct{ name, input, want string }{
		{"maximum_members", maximumMembers, maximumMembers},
		{"maximum_value", maximumValue, maximumValue},
		{"too_many_members", maximumMembers + ",extra=value", ""},
		{"oversized_value", maximumValue + "x", ""},
		{"control_character", "vendor=secret\nvalue", ""},
		{"duplicate_key", "vendor=a,vendor=b", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ExtractTrace(t.Context(), http.Header{"Traceparent": {parent}, "Tracestate": {tc.input}})
			gotParent, state := Serialize(ctx)
			require.Equal(t, parent, gotParent)
			require.Equal(t, tc.want, state)
			storedParent, storedState := Serialize(Restore(ctx, gotParent, state))
			require.Equal(t, gotParent, storedParent)
			require.Equal(t, state, storedState)
		})
	}
}
