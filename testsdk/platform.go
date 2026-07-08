// Package testsdk provides test helpers for integration-testing Declarion
// JSON-RPC sidecar handlers against a real platform instance.
//
// Two modes:
//   - Automatic: testcontainers-go starts Postgres + Declarion. Zero setup.
//   - External: DECLARION_TEST_URL points at an already-running instance.
package testsdk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/platform"
	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

// secretPrimaryKeyID is the key id used in the generated
// DECLARION_SECRET_KEYS (see randomSecretKeys).
const secretPrimaryKeyID = "v1"

// PlatformEnv holds a running Declarion platform for integration tests.
// Created once in TestMain, shared across all tests in the package.
type PlatformEnv struct {
	// URL is the base URL of the Declarion platform API.
	URL string

	// JWTSecret is the shared secret for minting continuation tokens.
	JWTSecret string

	databaseURL     string
	stopFn          func()
	logger          *zap.Logger
	serverContainer interface {
		Logs(context.Context) (io.ReadCloser, error)
	}
}

// ServerLogs returns the Declarion server container's stdout/stderr logs.
// Useful for debugging test failures.
func (e *PlatformEnv) ServerLogs() string {
	if e.serverContainer == nil {
		return ""
	}
	logs, err := e.serverContainer.Logs(context.Background())
	if err != nil {
		return fmt.Sprintf("error reading logs: %v", err)
	}
	defer func() { _ = logs.Close() }()
	b, _ := io.ReadAll(logs)
	return string(b)
}

// DBPool returns a pgx pool connected to the test platform database.
//
// In automatic container mode this connects to the Postgres container that
// StartPlatform already started and migrated. In external mode, set
// DECLARION_TEST_DATABASE_URL to the database behind DECLARION_TEST_URL.
func (e *PlatformEnv) DBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if e.databaseURL == "" {
		t.Fatal("testsdk: database URL is unavailable; set DECLARION_TEST_DATABASE_URL when using DECLARION_TEST_URL")
	}
	pool, err := pgxpool.New(context.Background(), e.databaseURL)
	if err != nil {
		t.Fatalf("testsdk: connect to platform database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Option configures StartPlatform.
type Option func(*config)

type config struct {
	moduleDir     string
	schemaDir     string
	migrationsDir string
	moduleName    string
	moduleNameSet bool
	image         string
	jwtSecret     string
	containerEnv  map[string]string
	logger        *zap.Logger
}

// WithModuleDir sets the consumer module root to copy into the Declarion container.
// The directory must contain manifest.yaml and use the same basename as the
// manifest's name. This is the preferred path for consumer integration tests
// because it exercises the same module layout that production images ship.
func WithModuleDir(dir string) Option {
	return func(c *config) { c.moduleDir = dir }
}

// WithSchema sets the consumer schema directory to copy into the Declarion container.
// Prefer WithModuleDir when the consumer has a real module root with manifest.yaml.
func WithSchema(dir string) Option {
	return func(c *config) { c.schemaDir = dir }
}

// WithMigrations sets the consumer migrations directory to copy into the Declarion container.
// Prefer WithModuleDir when the consumer has a real module root with manifest.yaml.
func WithMigrations(dir string) Option {
	return func(c *config) { c.migrationsDir = dir }
}

// WithModuleName sets the consumer module name (default: "test-consumer").
func WithModuleName(name string) Option {
	return func(c *config) {
		c.moduleName = name
		c.moduleNameSet = true
	}
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
func WithLogger(l *zap.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithContainerEnv sets additional env vars on the Declarion container.
// Use for setting platform parameters that have env_var declared in YAML
// (e.g. CLICKUP_API_TOKEN). These are resolved by the platform at request time.
func WithContainerEnv(env map[string]string) Option {
	return func(c *config) { c.containerEnv = env }
}

// StartPlatform starts a Declarion platform for integration tests.
//
// If DECLARION_TEST_URL is set, uses that URL (no containers started).
// Otherwise, starts Postgres + Declarion via testcontainers-go.
func StartPlatform(opts ...Option) (*PlatformEnv, error) {
	cfg := &config{
		image:      "declarion:latest",
		moduleName: "test-consumer",
		logger:     zap.NewNop(),
	}
	for _, o := range opts {
		o(cfg)
	}
	// Generate a fresh random JWT secret (32 bytes -> 64 hex chars, clears
	// core's HMAC minimum) unless a test supplied one via WithJWTSecret. No
	// secret material is hardcoded in testsdk.
	if cfg.jwtSecret == "" {
		cfg.jwtSecret = randomHex(32)
	}

	// External mode: use an already-running Declarion.
	if url := os.Getenv("DECLARION_TEST_URL"); url != "" {
		secret := os.Getenv("DECLARION_JWT_SECRET")
		if secret == "" {
			secret = cfg.jwtSecret
		}
		env := &PlatformEnv{
			URL:         strings.TrimRight(url, "/"),
			JWTSecret:   secret,
			databaseURL: os.Getenv("DECLARION_TEST_DATABASE_URL"),
			stopFn:      func() {},
			logger:      cfg.logger,
		}
		if err := env.waitForHealth(10 * time.Second); err != nil {
			return nil, fmt.Errorf("external platform not healthy: %w", err)
		}
		cfg.logger.Info("using external platform", zap.String("url", env.URL))
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
	tenantID     string
	tenantCode   string
	userID       string
	isGlobalUser bool
}

// WithTenant sets the test tenant code. The tenant ID stays the bootstrapped
// system tenant's; use WithGlobalTenant to actually stand in `_global`.
func WithTenant(code string) CtxOption {
	return func(c *ctxConfig) { c.tenantCode = code }
}

// WithUser sets the test user ID.
func WithUser(id string) CtxOption {
	return func(c *ctxConfig) { c.userID = id }
}

// WithGlobalTenant mints the context standing in the `_global` tenant
// (tenant ID = the all-zeros sentinel) instead of the bootstrapped system
// tenant. Required for writes to `access: superadmin` entities (e.g. `user`,
// `tenant`) since core's tenant-scoped authority model gates those on
// STANDING in `_global`, not merely an IsSuperadmin claim (declarion-core
// docs/authority-model.md; plan 2026-07-01 Task 11).
func WithGlobalTenant() CtxOption {
	return func(c *ctxConfig) {
		c.tenantID = globalTenantID
		c.tenantCode = globalTenantCode
		c.isGlobalUser = true
	}
}

// NewCtx creates a handler context for a test. Uses the bootstrapped system
// tenant. The returned *runtime.HandlerCtx has a valid continuation token and platform client.
func (e *PlatformEnv) NewCtx(t *testing.T, opts ...CtxOption) *runtime.HandlerCtx {
	t.Helper()

	cfg := &ctxConfig{
		tenantID:   systemTenantID,
		tenantCode: systemTenantCode,
		userID:     systemUserID,
	}
	for _, o := range opts {
		o(cfg)
	}

	token := e.mintToken(cfg.tenantID, cfg.tenantCode, cfg.userID, cfg.isGlobalUser)

	platClient := platform.New(platform.Config{
		BaseURL: e.URL,
		Token:   token,
	})

	ctx := &runtime.HandlerCtx{
		Context:    context.Background(),
		Platform:   platClient,
		Logger:     zap.NewNop().With(zap.String("test", t.Name()), zap.String("tenant", cfg.tenantCode)),
		TenantID:   cfg.tenantID,
		TenantCode: cfg.tenantCode,
		UserID:     cfg.userID,
		AuditOp:    fmt.Sprintf("test-%s", t.Name()),
		Action:     "test",
	}

	return ctx
}

// SetParam is reserved for future per-test param overrides via the platform API.
// For now, use WithContainerEnv in StartPlatform to set params at container startup.
// The platform resolves env vars declared in the consumer's parameters YAML.
func (e *PlatformEnv) SetParam(t *testing.T, ctx *runtime.HandlerCtx, code string, value any) {
	t.Helper()
	t.Logf("SetParam %q=%v (requires WithContainerEnv at startup for env-backed params)", code, value)
}

func (e *PlatformEnv) mintToken(tenantID, tenantCode, userID string, isGlobalUser bool) string {
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
		TenantID:   tenantID,
		TenantCode: tenantCode,
		Action:     "test",
		AuditOpID:  "test-audit",
		Scope:      runtime.HandlerTokenScope,
		// The test actor is a superadmin: integration tests exercise handler
		// logic against a real platform, not the RBAC layer, so the actor
		// carries full authority and never fights permissions/role seeds.
		// Core authorizes handler tokens from these signed claims.
		IsSuperadmin: true,
		// IsGlobalUser set only when standing in `_global` (WithGlobalTenant),
		// matching what a real `_global`-tenant session carries. The
		// `access: superadmin` entity write floor keys off TenantID alone
		// (checkEntityAccess/EntityAccessSuperadmin), but other cross-tenant
		// paths in core's tenant-scoped authority model key off this flag.
		IsGlobalUser: isGlobalUser,
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
