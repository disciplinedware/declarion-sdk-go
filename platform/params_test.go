package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// paramServer answers GET /api/params/{code} with one canned envelope.
func paramServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL})
}

// TestGetParam_typedReads pins what a consumer receives for each of the three
// typed parameters: a decimal as its canonical TEXT, an enum as the member
// code, a list as a []string.
func TestGetParam_typedReads(t *testing.T) {
	t.Run("decimal arrives as exact text", func(t *testing.T) {
		// More significant digits than a float64 carries. Reading it as a
		// string is what keeps them.
		c := paramServer(t, `{"data":{"code":"unit_price","found":true,"value":"123456789012345678901.5","source":"tenant","type":"decimal"}}`)
		got, err := GetParam[string](c.Params(), t.Context(), "unit_price", "")
		if err != nil {
			t.Fatalf("GetParam: %v", err)
		}
		if got != "123456789012345678901.5" {
			t.Errorf("value: got %q, want the exact text", got)
		}
	})

	t.Run("enum arrives as the member code", func(t *testing.T) {
		c := paramServer(t, `{"data":{"code":"mailer_backend","found":true,"value":"smtp","source":"tenant","type":"enum"}}`)
		got, err := GetParam[string](c.Params(), t.Context(), "mailer_backend", "noop")
		if err != nil {
			t.Fatalf("GetParam: %v", err)
		}
		if got != "smtp" {
			t.Errorf("value: got %q, want smtp", got)
		}
	})

	t.Run("list arrives as a string slice", func(t *testing.T) {
		c := paramServer(t, `{"data":{"code":"allowed_models","found":true,"value":["b","a"],"source":"tenant","type":"string_array"}}`)
		got, err := GetParam[[]string](c.Params(), t.Context(), "allowed_models", nil)
		if err != nil {
			t.Fatalf("GetParam: %v", err)
		}
		if len(got) != 2 || got[0] != "b" || got[1] != "a" {
			t.Errorf("value: got %#v, want the written order [b a]", got)
		}
	})
}

// TestGetParam_decimalAsFloatIsRefused pins the direction documentation cannot
// enforce: a decimal read into a float type FAILS rather than rounding
// quietly. The text is exact and a float64 is not.
func TestGetParam_decimalAsFloatIsRefused(t *testing.T) {
	c := paramServer(t, `{"data":{"code":"unit_price","found":true,"value":"19.99","source":"tenant","type":"decimal"}}`)
	if _, err := GetParam[float64](c.Params(), t.Context(), "unit_price", 0); err == nil {
		t.Fatal("a decimal read as float64 was accepted; the digits would be lost silently")
	}

	// The control: a float read is not refused in general - it is refused
	// because the decimal's canonical form is TEXT. Without this half the case
	// above would pass against an SDK whose float reads never work at all.
	f := paramServer(t, `{"data":{"code":"slo_target","found":true,"value":99.95,"source":"tenant","type":"float"}}`)
	got, err := GetParam[float64](f.Params(), t.Context(), "slo_target", 0)
	if err != nil {
		t.Fatalf("a float parameter read as float64 failed: %v", err)
	}
	if got != 99.95 {
		t.Errorf("float value: got %v, want 99.95", got)
	}
}

// TestLookup_absentIsNotZero is the "charges nothing" failure, pinned.
//
// GetParam cannot tell an absent value from a deliberate one - it returns the
// caller's default and reports success - so a missing price read with a zero
// default returns zero and looks fine. Lookup is what a money read must use,
// and this asserts BOTH halves: GetParam really does substitute, and Lookup
// really does report the absence.
func TestLookup_absentIsNotZero(t *testing.T) {
	c := paramServer(t, `{"data":{"code":"unit_price","found":false}}`)

	substituted, err := GetParam[string](c.Params(), t.Context(), "unit_price", "0")
	if err != nil {
		t.Fatalf("GetParam: %v", err)
	}
	if substituted != "0" {
		t.Fatalf("GetParam substituted %q, want the caller's default", substituted)
	}

	_, found, _, err := c.Params().Lookup(t.Context(), "unit_price")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found {
		t.Fatal("Lookup reported a value for an unset parameter")
	}
}
