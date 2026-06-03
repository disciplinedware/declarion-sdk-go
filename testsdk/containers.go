package testsdk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

// startContainers starts Postgres + Declarion via testcontainers-go on a shared network.
func startContainers(cfg *config) (*PlatformEnv, error) {
	ctx := context.Background()

	// Create a shared network so Declarion can reach Postgres by container name.
	net, err := network.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Start Postgres with a network alias.
	pgContainer, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("declarion"),
		postgres.WithUsername("declarion"),
		postgres.WithPassword("declarion"),
		network.WithNetwork([]string{"test-pg"}, net),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	// Declarion connects to Postgres via the internal network alias.
	dbURL := "postgres://declarion:declarion@test-pg:5432/declarion?sslmode=disable"

	// Build module mount (manifest + schema + migrations).
	mm, err := buildModuleMount(cfg)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("build module mount: %w", err)
	}
	cleanupModuleDir := func() {}
	if mm != nil {
		cleanupModuleDir = mm.cleanup
	}

	// Core fail-closes on an unset DECLARION_MODULES (>= 0.4.7): every container
	// that boots the binary - the migrate one-shot AND the API server - must
	// name an explicit module allowlist or it refuses to start. testsdk already
	// knows the consumer's module via WithModuleName, so derive _platform + that
	// module: an isolated per-consumer set that leaves the platform domain
	// modules shipped in the image (agents, commerce) inactive. A caller may
	// override by passing DECLARION_MODULES through WithContainerEnv.
	moduleSelector := "_platform," + cfg.moduleName
	if v, ok := cfg.containerEnv["DECLARION_MODULES"]; ok && strings.TrimSpace(v) != "" {
		moduleSelector = v
	}

	// Run migrations first via a one-shot container.
	migrateReq := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: cfg.image,
			Env: map[string]string{
				"DECLARION_DATABASE_URL": dbURL,
				"DECLARION_MODULES_DIR":  "/app/modules",
				"DECLARION_MODULES":      moduleSelector,
			},
			Cmd:        []string{"./declarion", "migrate", "apply"},
			WaitingFor: wait.ForExit().WithExitTimeout(60 * time.Second),
		},
		Started: true,
	}
	attachModuleBinds(&migrateReq, mm)
	if err := network.WithNetwork([]string{"test-migrate"}, net)(&migrateReq); err != nil {
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("configure migration network: %w", err)
	}

	migrateContainer, err := testcontainers.GenericContainer(ctx, migrateReq)
	if err != nil {
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Check exit code and capture logs on failure.
	state, err := migrateContainer.State(ctx)
	if err != nil || state.ExitCode != 0 {
		logs, _ := migrateContainer.Logs(ctx)
		var logMsg string
		if logs != nil {
			logBytes, _ := io.ReadAll(logs)
			_ = logs.Close()
			logMsg = string(logBytes)
		}
		_ = migrateContainer.Terminate(ctx)
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		if err != nil {
			return nil, fmt.Errorf("check migration state: %w", err)
		}
		return nil, fmt.Errorf("migrations failed with exit code %d: %s", state.ExitCode, logMsg)
	}
	// Log successful migration output.
	migrateLogs, _ := migrateContainer.Logs(ctx)
	if migrateLogs != nil {
		logBytes, _ := io.ReadAll(migrateLogs)
		_ = migrateLogs.Close()
		cfg.logger.Info("migration completed", zap.String("logs", string(logBytes)))
	}
	_ = migrateContainer.Terminate(ctx)

	// Start the Declarion API server.
	serverEnv := map[string]string{
		"DECLARION_DATABASE_URL":   dbURL,
		"DECLARION_JWT_SECRET":     cfg.jwtSecret,
		"DECLARION_ROLES":          "api",
		"DECLARION_MODULES_DIR":    "/app/modules",
		"DECLARION_MODULES":        moduleSelector,
		"DECLARION_SECRET_KEYS":    randomSecretKeys(),
		"DECLARION_SECRET_PRIMARY": secretPrimaryKeyID,
	}
	// Effectively disable rate limiting in the test container - integration
	// tests hammer the API and would otherwise hit HTTP 429. Uses the
	// platform's standard DECLARION_RATE_LIMIT_* env mechanism (the same
	// knobs core's compose sets high for local/e2e), so nothing bespoke.
	for k, v := range testRateLimitEnv() {
		serverEnv[k] = v
	}
	for k, v := range cfg.containerEnv {
		serverEnv[k] = v
	}
	declarionReq := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        cfg.image,
			Env:          serverEnv,
			ExposedPorts: []string{"3000/tcp"},
			WaitingFor: wait.ForHTTP("/api/health").
				WithPort("3000/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	}
	attachModuleBinds(&declarionReq, mm)
	if err := network.WithNetwork([]string{"test-declarion"}, net)(&declarionReq); err != nil {
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("configure declarion network: %w", err)
	}

	declarionContainer, err := testcontainers.GenericContainer(ctx, declarionReq)
	if err != nil {
		// Surface the container's own logs: a boot rejection (e.g. core
		// tightening a required env var) otherwise reads as an opaque
		// "container exited with code 1". The migrator path does the same.
		logMsg := containerLogs(ctx, declarionContainer)
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		if logMsg != "" {
			return nil, fmt.Errorf("start declarion container: %w\n--- declarion container logs ---\n%s", err, logMsg)
		}
		return nil, fmt.Errorf("start declarion container: %w", err)
	}

	declarionHost, err := declarionContainer.Host(ctx)
	if err != nil {
		_ = declarionContainer.Terminate(ctx)
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get declarion host: %w", err)
	}
	declarionPort, err := declarionContainer.MappedPort(ctx, "3000/tcp")
	if err != nil {
		_ = declarionContainer.Terminate(ctx)
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get declarion port: %w", err)
	}

	url := fmt.Sprintf("http://%s:%s", declarionHost, declarionPort.Port())

	// Bootstrap: create the system tenant + owner user via SQL.
	// This is the same pattern as the platform's initial setup - the first
	// tenant must exist before any API call can succeed (auth requires tenant_id).
	if err := bootstrapTenant(ctx, pgContainer, net); err != nil {
		_ = declarionContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		cleanupModuleDir()
		return nil, fmt.Errorf("bootstrap tenant: %w", err)
	}

	cfg.logger.Info("platform started", zap.String("url", url))

	env := &PlatformEnv{
		URL:             url,
		JWTSecret:       cfg.jwtSecret,
		logger:          cfg.logger,
		serverContainer: declarionContainer,
		stopFn: func() {
			termCtx := context.Background()
			if err := declarionContainer.Terminate(termCtx); err != nil {
				cfg.logger.Warn("stop declarion container", zap.Error(err))
			}
			if err := pgContainer.Terminate(termCtx); err != nil {
				cfg.logger.Warn("stop postgres container", zap.Error(err))
			}
			_ = net.Remove(termCtx)
			cleanupModuleDir()
		},
	}

	return env, nil
}

const (
	systemTenantID   = "00000000-0000-0000-0000-000000000001"
	systemTenantCode = "test"
	systemUserID     = "00000000-0000-0000-0000-000000000002"
)

// bootstrapTenant creates the initial tenant + owner user in the DB via psql.
func bootstrapTenant(ctx context.Context, pgContainer *postgres.PostgresContainer, net *testcontainers.DockerNetwork) error {
	sql := fmt.Sprintf(`
		INSERT INTO declarion.tenants (id, code, name)
		VALUES ('%s', '%s', '{"en":"System Test Tenant"}')
		ON CONFLICT (id) DO NOTHING;
	`, systemTenantID, systemTenantCode)

	exitCode, output, err := pgContainer.Exec(ctx, []string{
		"psql", "-U", "declarion", "-d", "declarion", "-c", sql,
	})
	if err != nil {
		return fmt.Errorf("exec psql: %w", err)
	}
	if exitCode != 0 {
		outBytes, _ := io.ReadAll(output)
		return fmt.Errorf("psql exit %d: %s", exitCode, string(outBytes))
	}
	return nil
}

// moduleMount holds the resolved paths and manifest for mounting into containers.
type moduleMount struct {
	manifestDir string // temp dir containing manifest.yaml
	schemaDir   string // absolute path to schema/
	migrDir     string // absolute path to migrations/
	moduleName  string
	cleanup     func()
}

// buildModuleMount resolves paths and creates the manifest.yaml in a temp dir.
func buildModuleMount(cfg *config) (*moduleMount, error) {
	if cfg.schemaDir == "" && cfg.migrationsDir == "" {
		return nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "testsdk-manifest-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	manifest := fmt.Sprintf(
		"name: %s\nkind: application\nversion: \"0.0.0-test\"\nrevision: \"test\"\nbuild_time: \"%s\"\n",
		cfg.moduleName, now,
	)
	if err := os.WriteFile(filepath.Join(tmpDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	m := &moduleMount{
		manifestDir: tmpDir,
		moduleName:  cfg.moduleName,
		cleanup:     func() { _ = os.RemoveAll(tmpDir) },
	}

	if cfg.schemaDir != "" {
		m.schemaDir, err = filepath.Abs(cfg.schemaDir)
		if err != nil {
			m.cleanup()
			return nil, fmt.Errorf("resolve schema dir: %w", err)
		}
	}
	if cfg.migrationsDir != "" {
		m.migrDir, err = filepath.Abs(cfg.migrationsDir)
		if err != nil {
			m.cleanup()
			return nil, fmt.Errorf("resolve migrations dir: %w", err)
		}
	}
	return m, nil
}
func attachModuleBinds(req *testcontainers.GenericContainerRequest, m *moduleMount) {
	if req == nil || m == nil {
		return
	}
	binds := m.binds()
	prev := req.HostConfigModifier
	req.HostConfigModifier = func(hostConfig *container.HostConfig) {
		if prev != nil {
			prev(hostConfig)
		}
		hostConfig.Binds = append(hostConfig.Binds, binds...)
	}
}

// binds returns the host binds for a container. Each real directory gets its
// own mount (no symlinks - Docker bind mounts don't follow host-side symlinks).
func (m *moduleMount) binds() []string {
	base := fmt.Sprintf("/app/modules/%s", m.moduleName)
	result := []string{
		// Manifest lives in the temp dir.
		fmt.Sprintf("%s:%s", filepath.Join(m.manifestDir, "manifest.yaml"), base+"/manifest.yaml"),
	}
	if m.schemaDir != "" {
		result = append(result, fmt.Sprintf("%s:%s", m.schemaDir, base+"/schema"))
	}
	if m.migrDir != "" {
		result = append(result, fmt.Sprintf("%s:%s", m.migrDir, base+"/migrations"))
	}
	return result
}

// randomHex returns a hex string of n cryptographically-random bytes (2n
// chars). Used for the per-run test JWT secret so nothing secret-shaped is
// hardcoded and the value always clears core's minimum-length check.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomSecretKeys returns a DECLARION_SECRET_KEYS JSON value carrying a
// single random 32-byte AES-256 key under id secretPrimaryKeyID. Generated
// per run - the platform secret codec needs a valid key to boot; testsdk
// never decrypts with it, so a throwaway random key is sufficient.
func randomSecretKeys() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return fmt.Sprintf(`{%q:%q}`, secretPrimaryKeyID, base64.StdEncoding.EncodeToString(b))
}

// testRateLimitEnv returns DECLARION_RATE_LIMIT_* overrides that give the
// test container effectively-unlimited token buckets, so integration tests
// (which hammer the API) never hit HTTP 429. Reuses the platform's standard
// per-group rate-limit env params (resolver: DECLARION_<UPPER(code)>); the
// key_strategy per param matches the schema default so each JSON value
// passes validation. Overridable via WithContainerEnv like any other env.
func testRateLimitEnv() map[string]string {
	strategy := map[string]string{
		"DECLARION_RATE_LIMIT_PUBLIC":           "ip",
		"DECLARION_RATE_LIMIT_USER_NORMAL":      "user_id_or_ip",
		"DECLARION_RATE_LIMIT_BASIC_AUTH":       "ip",
		"DECLARION_RATE_LIMIT_DEFAULT_ACTION":   "ip_and_handler",
		"DECLARION_RATE_LIMIT_AUTH_ATTEMPT":     "ip_and_handler",
		"DECLARION_RATE_LIMIT_AUTH_SESSION":     "ip_and_handler",
		"DECLARION_RATE_LIMIT_EMAIL_OUTBOUND":   "ip_and_handler",
		"DECLARION_RATE_LIMIT_TOKEN_REDEEM":     "ip_and_handler",
		"DECLARION_RATE_LIMIT_SERVICE_INTERNAL": "user_id",
	}
	out := make(map[string]string, len(strategy))
	for envName, keyStrategy := range strategy {
		out[envName] = fmt.Sprintf(`{"burst_capacity":1000000,"refill_per_second":1000000,"key_strategy":%q}`, keyStrategy)
	}
	return out
}

// containerLogs best-effort reads a container's combined logs for inclusion
// in an error message. Returns "" if logs are unavailable.
func containerLogs(ctx context.Context, c testcontainers.Container) string {
	if c == nil {
		return ""
	}
	r, err := c.Logs(ctx)
	if err != nil || r == nil {
		return ""
	}
	defer func() { _ = r.Close() }()
	out, _ := io.ReadAll(r)
	return string(out)
}
