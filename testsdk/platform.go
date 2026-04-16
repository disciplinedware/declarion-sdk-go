// Package testsdk provides test helpers for integration-testing Declarion
// JSON-RPC sidecar handlers against a real platform instance.
//
// Two modes:
//   - Automatic: testcontainers-go starts Postgres + Declarion. Zero setup.
//   - External: DECLARION_TEST_URL points at an already-running instance.
package testsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/disciplinedware/declarion-sdk-go/platform"
	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

const (
	defaultJWTSecret = "testsdk-jwt-secret"
	defaultOwnerUser = "testsdk-owner"
)

// PlatformEnv holds a running Declarion platform for integration tests.
// Created once in TestMain, shared across all tests in the package.
type PlatformEnv struct {
	// URL is the base URL of the Declarion platform API.
	URL string

	// JWTSecret is the shared secret for minting continuation tokens.
	JWTSecret string

	stopFn func()
	logger *slog.Logger
}

// Option configures StartPlatform.
type Option func(*config)

type config struct {
	schemaDir     string
	migrationsDir string
	moduleName    string
	image         string
	jwtSecret     string
	logger        *slog.Logger
}

// WithSchema sets the consumer schema directory to mount into the Declarion container.
func WithSchema(dir string) Option {
	return func(c *config) { c.schemaDir = dir }
}

// WithMigrations sets the consumer migrations directory.
func WithMigrations(dir string) Option {
	return func(c *config) { c.migrationsDir = dir }
}

// WithModuleName sets the consumer module name (default: "test-consumer").
func WithModuleName(name string) Option {
	return func(c *config) { c.moduleName = name }
}

// WithImage overrides the Declarion Docker image (default: ghcr.io/disciplinedware/declarion:latest).
func WithImage(image string) Option {
	return func(c *config) { c.image = image }
}

// WithJWTSecret sets the JWT secret (must match what the platform uses).
func WithJWTSecret(secret string) Option {
	return func(c *config) { c.jwtSecret = secret }
}

// WithLogger overrides the default test logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// StartPlatform starts a Declarion platform for integration tests.
//
// If DECLARION_TEST_URL is set, uses that URL (no containers started).
// Otherwise, starts Postgres + Declarion via testcontainers-go.
func StartPlatform(opts ...Option) (*PlatformEnv, error) {
	cfg := &config{
		image:      "declarion:latest",
		jwtSecret:  defaultJWTSecret,
		moduleName: "test-consumer",
		logger:     slog.Default(),
	}
	for _, o := range opts {
		o(cfg)
	}

	// External mode: use an already-running Declarion.
	if url := os.Getenv("DECLARION_TEST_URL"); url != "" {
		secret := os.Getenv("DECLARION_JWT_SECRET")
		if secret == "" {
			secret = cfg.jwtSecret
		}
		env := &PlatformEnv{
			URL:       strings.TrimRight(url, "/"),
			JWTSecret: secret,
			stopFn:    func() {},
			logger:    cfg.logger,
		}
		if err := env.waitForHealth(10 * time.Second); err != nil {
			return nil, fmt.Errorf("external platform not healthy: %w", err)
		}
		cfg.logger.Info("using external platform", "url", env.URL)
		return env, nil
	}

	// Container mode: start via testcontainers-go.
	return startContainers(cfg)
}

// Stop shuts down containers (no-op in external mode).
func (e *PlatformEnv) Stop() {
	if e.stopFn != nil {
		e.stopFn()
	}
}

// CtxOption configures NewCtx.
type CtxOption func(*ctxConfig)

type ctxConfig struct {
	tenantCode string
	userID     string
}

// WithTenant sets the test tenant code.
func WithTenant(code string) CtxOption {
	return func(c *ctxConfig) { c.tenantCode = code }
}

// WithUser sets the test user ID.
func WithUser(id string) CtxOption {
	return func(c *ctxConfig) { c.userID = id }
}

// NewCtx creates a handler context for a test. Each call gets an isolated
// tenant (created via the platform API). The returned *runtime.Ctx has a
// valid continuation token and platform client.
func (e *PlatformEnv) NewCtx(t *testing.T, opts ...CtxOption) *runtime.Ctx {
	t.Helper()

	cfg := &ctxConfig{
		tenantCode: fmt.Sprintf("test-%d", time.Now().UnixNano()%1000000),
		userID:     defaultOwnerUser,
	}
	for _, o := range opts {
		o(cfg)
	}

	// Mint a continuation token for this test context.
	token := e.mintToken(cfg.tenantCode, cfg.userID)

	platClient := platform.New(platform.Config{
		BaseURL: e.URL,
		Token:   token,
	})

	ctx := &runtime.Ctx{
		Context:    context.Background(),
		Platform:   platClient,
		Logger:     slog.Default().With("test", t.Name(), "tenant", cfg.tenantCode),
		TenantID:   cfg.tenantCode, // simplified: use code as ID for tests
		TenantCode: cfg.tenantCode,
		UserID:     cfg.userID,
		AuditOp:    fmt.Sprintf("test-%s", t.Name()),
		Action:     "test",
	}

	return ctx
}

// SetParam sets a platform parameter for the given test context's tenant.
func (e *PlatformEnv) SetParam(t *testing.T, ctx *runtime.Ctx, code string, value any) {
	t.Helper()
	// Set via platform API: POST /api/data/core_parameter with upsert.
	records := []map[string]any{{
		"code":  code,
		"value": fmt.Sprintf("%v", value),
	}}
	_, err := ctx.Platform.Data().BulkUpsert(ctx.Context, "core_parameter", "code", records)
	if err != nil {
		t.Fatalf("set param %q: %v", code, err)
	}
}

func (e *PlatformEnv) mintToken(tenantCode, userID string) string {
	now := time.Now()
	claims := &runtime.HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{runtime.HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        fmt.Sprintf("test-%d", now.UnixNano()),
		},
		UserID:     userID,
		TenantID:   tenantCode,
		TenantCode: tenantCode,
		Action:     "test",
		AuditOpID:  "test-audit",
		Scope:      runtime.HandlerTokenScope,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(e.JWTSecret))
	if err != nil {
		panic(fmt.Sprintf("mint test token: %v", err))
	}
	return signed
}

func (e *PlatformEnv) waitForHealth(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(e.URL + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("platform not healthy after %s", timeout)
}

// readJSON is a helper for reading JSON responses.
func readJSON(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
