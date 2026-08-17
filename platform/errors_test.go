package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

// TestAnInterposerIsNotBelieved pins what the media-type check is for: a proxy,
// a load balancer or a gateway answering INSTEAD of the platform must not have
// its page read as the platform's error object, while a real answer must survive
// whatever header the deployment happens to send.
//
// Red in both directions on purpose - a check that only ever refuses is as wrong
// as one that only ever accepts.
func TestAnInterposerIsNotBelieved(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		want        bool
	}{
		{"the platform's own media type", ProblemContentType, true},
		{"with a charset parameter", ProblemContentType + "; charset=utf-8", true},
		{"plain JSON - what an interposer sends, and what a deployment older than this contract sends", "application/json", false},
		{"no header at all - the platform always names its media type", "", false},
		{"a gateway's HTML error page", "text/html; charset=utf-8", false},
		{"a load balancer's plain text", "text/plain", false},
		{"an unparseable header", "application/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProblemResponse(tc.contentType); got != tc.want {
				t.Fatalf("isProblemResponse(%q) = %v, want %v", tc.contentType, got, tc.want)
			}
		})
	}
}

// A body that clears the media-type check still has to name a DECLARED type.
// Together the two are the gate: the header says who is speaking, the code says
// they said something this SDK can act on.
func TestAJSONBodyWithNoDeclaredCodeIsUnreadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The platform's OWN media type, so the BODY is what this test measures.
		// With any other, the header alone decides and the test would pass
		// having never reached the check it is named after.
		w.Header().Set("Content-Type", ProblemContentType)
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"message":"upstream connect error"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().List(t.Context(), "lead", ListParams{})
	e, ok := errs.From(err)
	if !ok {
		t.Fatalf("want an error object, got %v", err)
	}
	if e.Code() != TypeUnreadableResponse {
		t.Fatalf("type: got %q, want %q", e.Code(), TypeUnreadableResponse)
	}
}
