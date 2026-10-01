package platform

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// paramsPlatform answers every request with the parameter version it holds now,
// and a parameter read with the max age it holds now; it counts parameter reads.
type paramsPlatform struct {
	srv      *httptest.Server
	version  atomic.Int64
	maxAgeMs atomic.Int64
	reads    atomic.Int64
}

func newParamsPlatform(t *testing.T, body string) *paramsPlatform {
	t.Helper()
	p := &paramsPlatform{}
	p.version.Store(1)
	p.maxAgeMs.Store(60_000)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(ParamsVersionHeader, strconv.FormatInt(p.version.Load(), 10))
		if len(r.URL.Path) > len("/api/params/") && r.URL.Path[:len("/api/params/")] == "/api/params/" {
			p.reads.Add(1)
			if ms := p.maxAgeMs.Load(); ms >= 0 {
				w.Header().Set(ParamsMaxAgeHeader, strconv.FormatInt(ms, 10))
			}
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *paramsPlatform) client(token, tenantID string) *Client {
	return New(Config{BaseURL: p.srv.URL, Token: token, TargetTenantID: tenantID, HTTPClient: p.srv.Client()})
}

func lookup(t *testing.T, c *Client, code string) any {
	t.Helper()
	v, found, _, err := c.Params().Lookup(t.Context(), code)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !found {
		t.Fatal("Lookup: not found")
	}
	return v
}

const listBody = `{"data":{"code":"allowed_models","found":true,"value":["a","b"],"source":"tenant"}}`

func TestLookup_aReadIsReusedAcrossClientsOfOneIdentity(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	lookup(t, p.client("tok", "t1"), "allowed_models")
	lookup(t, p.client("tok", "t1"), "allowed_models")
	if got := p.reads.Load(); got != 1 {
		t.Fatalf("reads: got %d, want 1 - a second Client of the same identity must hit the cache", got)
	}
}

func TestLookup_aNewerVersionOnAnyResponseEndsTheReuse(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	c := p.client("tok", "t1")
	lookup(t, c, "allowed_models")

	p.version.Store(2)
	if _, err := c.Data().List(t.Context(), "user", ListParams{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	lookup(t, c, "allowed_models")
	if got := p.reads.Load(); got != 2 {
		t.Fatalf("reads: got %d, want 2 - the List response carried version 2", got)
	}
	lookup(t, c, "allowed_models")
	if got := p.reads.Load(); got != 2 {
		t.Fatalf("reads: got %d, want 2 - the read at version 2 is cached again", got)
	}
}

func TestLookup_aReadOlderThanAVersionAlreadyHeardIsNotKept(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	c := p.client("tok", "t1")
	p.version.Store(7)
	if _, err := c.Data().List(t.Context(), "user", ListParams{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	p.version.Store(5) // a pod whose listener has not caught up
	lookup(t, c, "allowed_models")
	lookup(t, c, "allowed_models")
	if got := p.reads.Load(); got != 2 {
		t.Fatalf("reads: got %d, want 2 - an answer at version 5 is stale against 7", got)
	}
}

func TestLookup_aReadIsNotReusedPastItsMaxAge(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	p.maxAgeMs.Store(1)
	c := p.client("tok", "t1")
	lookup(t, c, "allowed_models")
	time.Sleep(5 * time.Millisecond)
	lookup(t, c, "allowed_models")
	if got := p.reads.Load(); got != 2 {
		t.Fatalf("reads: got %d, want 2", got)
	}
}

func TestLookup_aReadWithoutAMaxAgeIsNeverReused(t *testing.T) {
	for name, maxAgeMs := range map[string]int64{"absent": -1, "zero": 0} {
		t.Run(name, func(t *testing.T) {
			p := newParamsPlatform(t, listBody)
			p.maxAgeMs.Store(maxAgeMs)
			c := p.client("tok", "t1")
			lookup(t, c, "allowed_models")
			lookup(t, c, "allowed_models")
			if got := p.reads.Load(); got != 2 {
				t.Fatalf("reads: got %d, want 2", got)
			}
		})
	}
}

func TestLookup_anotherIdentityOrTenantReadsForItself(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	lookup(t, p.client("tok", "t1"), "allowed_models")
	lookup(t, p.client("other", "t1"), "allowed_models")
	lookup(t, p.client("tok", "t2"), "allowed_models")
	if got := p.reads.Load(); got != 3 {
		t.Fatalf("reads: got %d, want 3 - the answer depends on who asks and where", got)
	}
}

func TestLookup_aCallerCannotChangeAnotherCallersAnswer(t *testing.T) {
	p := newParamsPlatform(t, listBody)
	c := p.client("tok", "t1")
	first := lookup(t, c, "allowed_models").([]any)
	first[0] = "changed"
	second := lookup(t, c, "allowed_models").([]any)
	if second[0] != "a" {
		t.Fatalf("second answer: got %v, want the platform's [a b]", second)
	}
	if got := p.reads.Load(); got != 1 {
		t.Fatalf("reads: got %d, want 1", got)
	}
}

func TestLookup_anAbsentValueIsCachedAsAbsent(t *testing.T) {
	p := newParamsPlatform(t, `{"data":{"code":"unit_price","found":false}}`)
	c := p.client("tok", "t1")
	for range 2 {
		v, found, _, err := c.Params().Lookup(t.Context(), "unit_price")
		if err != nil || found || v != nil {
			t.Fatalf("Lookup: v=%v found=%v err=%v, want absent", v, found, err)
		}
	}
	if got := p.reads.Load(); got != 1 {
		t.Fatalf("reads: got %d, want 1", got)
	}
}

func TestParamsCache_dropsWhatItCanNoLongerServe(t *testing.T) {
	pc := &paramsCache{versions: map[string]int64{"u": 1}, entries: map[paramsKey]paramsEntry{}}
	h := http.Header{}
	h.Set(ParamsVersionHeader, "1")
	h.Set(ParamsMaxAgeHeader, "1000")
	now := time.Now()
	for i := range 100 {
		pc.put(paramsKey{baseURL: "u", token: strconv.Itoa(i)}, h, paramsEntry{}, now)
	}
	later := now.Add(2 * time.Second)
	for i := range 100 {
		pc.put(paramsKey{baseURL: "u", token: "late" + strconv.Itoa(i)}, h, paramsEntry{}, later)
	}
	if got := len(pc.entries); got > 150 {
		t.Fatalf("entries: got %d - the 100 expired ones were never dropped", got)
	}
}

// Two concurrent misses: the answer from a pod whose listener lags arrives
// last and must not replace the current one.
func TestParamsCache_aLateStaleAnswerDoesNotReplaceACurrentOne(t *testing.T) {
	pc := &paramsCache{versions: map[string]int64{}, entries: map[paramsKey]paramsEntry{}}
	key := paramsKey{baseURL: "u", code: "c"}
	now := time.Now()
	header := func(version string) http.Header {
		h := http.Header{}
		h.Set(ParamsVersionHeader, version)
		h.Set(ParamsMaxAgeHeader, "60000")
		return h
	}
	current, stale := header("7"), header("5")
	pc.observe("u", current)
	pc.put(key, current, paramsEntry{source: "current"}, now)
	pc.observe("u", stale)
	pc.put(key, stale, paramsEntry{source: "stale"}, now)

	entry, ok := pc.get(key, now)
	if !ok || entry.source != "current" {
		t.Fatalf("get: ok=%v source=%q, want the version-7 answer", ok, entry.source)
	}
}
