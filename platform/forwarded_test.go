package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The address travels on every call made under the context, so a service sets it
// once where it admits a request rather than at each call site.
func TestEveryCallUnderOneRequestCarriesItsOriginatingClient(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(ForwardedForHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "t"})
	ctx := WithOriginatingClientIP(context.Background(), "203.0.113.7:51514")
	req, err := c.newRequest(ctx, http.MethodGet, "/api/anything", nil, nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	if seen != "203.0.113.7" {
		t.Fatalf("the platform saw %q, want the originating client with its port stripped", seen)
	}
}

// A value that is not an address is DROPPED rather than sent. The header decides
// a rate-limit bucket, so a service passing something it did not verify must not
// be able to put arbitrary text in it.
func TestAnAddressThatDoesNotParseIsNotSent(t *testing.T) {
	for _, bad := range []string{
		"not-an-address",
		"203.0.113.7, 198.51.100.9", // a client-supplied chain, not one peer
		"<script>",
		" ",
	} {
		ctx := WithOriginatingClientIP(context.Background(), bad)
		if got := OriginatingClientIP(ctx); got != "" {
			t.Errorf("%q was kept as %q; only a single parseable address may travel", bad, got)
		}
	}
}

// Absent is absent: a call made outside any inbound request sends no header at
// all, rather than an empty one the platform would have to interpret.
func TestNoOriginatingClientSendsNoHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	setForwardedFor(req, context.Background())
	if _, present := req.Header[http.CanonicalHeaderKey(ForwardedForHeader)]; present {
		t.Fatal("a call with no originating client still sent the header")
	}
}
