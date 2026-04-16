package conformance

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

func TestGoSDKPassesConformanceSuite(t *testing.T) {
	// Build the conformance sidecar's handler registry.
	handlers := ConformanceSidecarHandlers()
	registry := make(map[string]runtime.HandlerRegistration, len(handlers))
	for _, h := range handlers {
		registry[h.Method] = h
	}

	// Start the sidecar in-process using httptest.
	cfg := &runtime.Config{}
	cfg.SetJWTSecret(jwtSecret) // same secret the harness uses
	cfg.ApplyDefaults()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		runtime.HandleRPCForTest(w, r, registry, cfg)
	})
	sidecar := httptest.NewServer(mux)
	defer sidecar.Close()

	// Run the conformance harness.
	h := NewHarness(sidecar.URL)

	// The harness starts a fake platform API internally. The sidecar needs to know
	// its URL so ctx.Platform can make callbacks. We set it via SetPlatformURL after
	// RunAll starts the fake API. Instead, we configure the sidecar to use the
	// harness's fake API URL by setting PlatformURL on the config.
	// Since the harness starts fakeAPI inside RunAll, we need a different approach:
	// pass the callback URL as a full URL in params, and the harness's fakeAPI
	// captures requests at any path. The sidecar's ctx.Platform is configured with
	// PlatformURL from the config, which the harness sets before calling.
	//
	// For in-process testing, we set PlatformURL dynamically: the harness's startFakeAPI
	// is called before tests, so we hook into it by running tests individually.
	// Simpler: set PlatformURL to empty string and let ctx.Platform.Data().Create() use
	// the callback_url param's base URL.
	//
	// Actually the cleanest approach: the harness sets PlatformURL on the sidecar config.
	// Since the harness creates fakeAPI in RunAll, we need to expose it.

	results := h.RunAllWithSidecarConfig(cfg)

	for _, r := range results {
		t.Run(r.Name, func(t *testing.T) {
			if !r.Passed {
				t.Errorf("FAIL: %s", r.Error)
			}
		})
	}

	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	require.NotEmpty(t, results, "conformance suite returned no test results")
	assert.Equal(t, len(results), passed, fmt.Sprintf("%d/%d tests passed", passed, len(results)))
}
