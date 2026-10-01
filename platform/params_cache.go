package platform

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// ParamsVersionHeader carries the platform's parameter version on every
	// authenticated response. It only grows, and any parameter write raises it.
	ParamsVersionHeader = "X-Declarion-Params-Version"
	// ParamsMaxAgeHeader on a parameter read says for how many milliseconds the
	// answer may be served from the cache; 0 or absent means not at all.
	ParamsMaxAgeHeader = "X-Declarion-Params-Max-Age-Ms"
)

// processParams is shared by every Client in the process, like the default
// transport: a consumer builds a Client per call and per tenant, and a cache per
// Client would never be hit.
var processParams = &paramsCache{
	versions: map[string]int64{},
	entries:  map[paramsKey]paramsEntry{},
}

// paramsCache answers Lookup without a request while the platform has said
// nothing newer. An entry is served only while it is younger than the max age
// the platform gave it AND it was read at the newest parameter version any
// response from that platform has carried since - so a parameter write is seen
// on the first read after any response that follows it.
type paramsCache struct {
	mu       sync.Mutex
	versions map[string]int64 // base URL -> newest ParamsVersionHeader received
	entries  map[paramsKey]paramsEntry
	sweepAt  int
}

// paramsKey names everything that changes an answer: the platform, the identity
// asking, the tenant it asks in, and the code.
type paramsKey struct {
	baseURL, token, tenantID, tenantCode, code string
}

type paramsEntry struct {
	// value stays encoded so every caller decodes its own copy: a map or slice
	// handed out twice would let one caller change another's answer.
	value   json.RawMessage
	found   bool
	source  string
	version int64
	expires time.Time
}

func headerInt(h http.Header, name string) (int64, bool) {
	raw := h.Get(name)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil
}

// observe records the parameter version a response from baseURL carried.
func (pc *paramsCache) observe(baseURL string, h http.Header) {
	version, ok := headerInt(h, ParamsVersionHeader)
	if !ok {
		return
	}
	pc.mu.Lock()
	if version > pc.versions[baseURL] {
		pc.versions[baseURL] = version
	}
	pc.mu.Unlock()
}

func (pc *paramsCache) get(key paramsKey, now time.Time) (paramsEntry, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	entry, ok := pc.entries[key]
	if !ok || entry.version != pc.versions[key.baseURL] || !now.Before(entry.expires) {
		return paramsEntry{}, false
	}
	return entry, true
}

// put keeps a read the platform allowed to be cached. A read stamped with a
// version older than one already received is stale on arrival and is not kept.
func (pc *paramsCache) put(key paramsKey, h http.Header, entry paramsEntry, now time.Time) {
	maxAge, ok := headerInt(h, ParamsMaxAgeHeader)
	if !ok || maxAge <= 0 {
		return
	}
	version, ok := headerInt(h, ParamsVersionHeader)
	if !ok {
		return
	}
	entry.version = version
	entry.expires = now.Add(time.Duration(maxAge) * time.Millisecond)

	pc.mu.Lock()
	defer pc.mu.Unlock()
	if version != pc.versions[key.baseURL] {
		return
	}
	pc.entries[key] = entry
	if len(pc.entries) >= pc.sweepAt {
		pc.dropUnservable(now)
		pc.sweepAt = 2 * len(pc.entries)
	}
}

// dropUnservable removes every entry get would refuse. Called when the map has
// doubled since the last call, so a process that reads with a fresh token per
// request does not grow without bound, at amortized constant cost per put.
func (pc *paramsCache) dropUnservable(now time.Time) {
	for key, entry := range pc.entries {
		if entry.version != pc.versions[key.baseURL] || !now.Before(entry.expires) {
			delete(pc.entries, key)
		}
	}
}

func (e paramsEntry) decode() (any, error) {
	if len(e.value) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(e.value, &v); err != nil {
		return nil, err
	}
	return v, nil
}
