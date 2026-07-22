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
	"regexp"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
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

	// Build the consumer module bundle copied into Declarion containers.
	mb, err := buildModuleBundle(cfg)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("build module bundle: %w", err)
	}
	cleanupModuleBundle := func() {}
	if mb != nil {
		cleanupModuleBundle = mb.cleanup
	}

	// Core fail-closes on an unset DECLARION_MODULES (>= 0.4.7): every container
	// that boots the binary - the migrate one-shot AND the API server - must
	// name an explicit module allowlist or it refuses to start. Core also
	// validates each module's depends_on is fully present and listed earlier
	// (>= 0.40.1), so the list must carry the consumer's whole declared
	// dependency set, not just the platform base. Derive it from the consumer
	// manifest's depends_on + the consumer module (see buildModuleSelector),
	// leaving other platform-domain modules in the image inactive. A caller may
	// override the whole value via WithContainerEnv("DECLARION_MODULES", ...).
	var moduleDependsOn []string
	moduleSelectorName := cfg.moduleName
	if mb != nil {
		moduleDependsOn = mb.dependsOn
		moduleSelectorName = mb.moduleName
	}
	moduleSelector := buildModuleSelector(moduleDependsOn, moduleSelectorName)
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
	attachModuleBundleFiles(&migrateReq, mb)
	if err := network.WithNetwork([]string{"test-migrate"}, net)(&migrateReq); err != nil {
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("configure migration network: %w", err)
	}

	migrateContainer, err := testcontainers.GenericContainer(ctx, migrateReq)
	if err != nil {
		cleanupModuleBundle()
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
		cleanupModuleBundle()
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

	// One AES key set shared by the seed one-shot and the API server: the seed
	// container boots the full app (mailer included), which fail-closes on an
	// empty DECLARION_SECRET_KEYS, and reusing the same keys keeps any encrypted
	// value written during seeding decryptable by the API role.
	secretKeys := randomSecretKeys()

	// Seed the platform bootstrap profile via a one-shot container, mirroring the
	// production migrator -> seeder -> api ordering. Since declarion-core 0.5.0 the
	// `api` role reconciles the per-tenant `tenant_bootstrap` profile at boot, and
	// that profile's platform-scheduler membership $lookup's the global
	// `platform-scheduler@declarion.local` technical user created by the `bootstrap`
	// profile. Without this step the API container fails to start with
	// "reconcile tenant_bootstrap (boot): ... got 0 rows". Scoped to
	// DECLARION_MODULES=declarion-core so it stays consumer-agnostic: it seeds
	// only the platform bootstrap (platform-scheduler user, _global + default
	// tenants) and never touches a consumer's bootstrap bundle (which may demand
	// its own env vars).
	seedReq := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: cfg.image,
			Env: map[string]string{
				"DECLARION_DATABASE_URL":   dbURL,
				"DECLARION_MODULES_DIR":    "/app/modules",
				"DECLARION_MODULES":        platformBaseModule,
				"DECLARION_SEED_PROFILES":  "bootstrap",
				"DECLARION_SECRET_KEYS":    secretKeys,
				"DECLARION_SECRET_PRIMARY": secretPrimaryKeyID,
			},
			Cmd:        []string{"./declarion", "seed", "apply"},
			WaitingFor: wait.ForExit().WithExitTimeout(60 * time.Second),
		},
		Started: true,
	}
	if err := network.WithNetwork([]string{"test-seed"}, net)(&seedReq); err != nil {
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("configure seed network: %w", err)
	}
	seedContainer, err := testcontainers.GenericContainer(ctx, seedReq)
	if err == nil {
		if state, serr := seedContainer.State(ctx); serr != nil {
			err = serr
		} else if state.ExitCode != 0 {
			err = fmt.Errorf("seed apply exited with code %d: %s", state.ExitCode, containerLogs(ctx, seedContainer))
		}
	}
	if err != nil {
		if seedContainer != nil {
			_ = seedContainer.Terminate(ctx)
		}
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("seed platform bootstrap: %w", err)
	}
	_ = seedContainer.Terminate(ctx)

	// Start the Declarion API server.
	serverEnv := map[string]string{
		"DECLARION_DATABASE_URL":   dbURL,
		"DECLARION_JWT_SECRET":     cfg.jwtSecret,
		"DECLARION_ROLES":          "api",
		"DECLARION_MODULES_DIR":    "/app/modules",
		"DECLARION_MODULES":        moduleSelector,
		"DECLARION_SECRET_KEYS":    secretKeys,
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
	attachModuleBundleFiles(&declarionReq, mb)
	if err := network.WithNetwork([]string{"test-declarion"}, net)(&declarionReq); err != nil {
		cleanupModuleBundle()
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
		cleanupModuleBundle()
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
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get declarion host: %w", err)
	}
	declarionPort, err := declarionContainer.MappedPort(ctx, "3000/tcp")
	if err != nil {
		_ = declarionContainer.Terminate(ctx)
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get declarion port: %w", err)
	}

	url := fmt.Sprintf("http://%s:%s", declarionHost, declarionPort.Port())
	pgHost, err := pgContainer.Host(ctx)
	if err != nil {
		_ = declarionContainer.Terminate(ctx)
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get postgres host: %w", err)
	}
	pgPort, err := pgContainer.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = declarionContainer.Terminate(ctx)
		cleanupModuleBundle()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		return nil, fmt.Errorf("get postgres port: %w", err)
	}
	hostDBURL := fmt.Sprintf("postgres://declarion:declarion@%s:%s/declarion?sslmode=disable", pgHost, pgPort.Port())

	// Bootstrap: create the system tenant + a real, DB-backed platform-operator
	// test actor via SQL. The first tenant must exist before any API call can
	// succeed (auth requires tenant_id); the actor needs REAL `_global`
	// ownership + the `platform_admin` role (not just a forged JWT claim)
	// because declarion-core's tenant-scoped authority model re-derives
	// authority live from the DB for async job drain - see
	// bootstrapTestPlatformActor's doc comment.
	if err := bootstrapTestPlatformActor(ctx, pgContainer, net); err != nil {
		_ = declarionContainer.Terminate(ctx)
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
		cleanupModuleBundle()
		return nil, fmt.Errorf("bootstrap test platform actor: %w", err)
	}

	cfg.logger.Info("platform started", zap.String("url", url))

	env := &PlatformEnv{
		URL:             url,
		JWTSecret:       cfg.jwtSecret,
		databaseURL:     hostDBURL,
		logger:          cfg.logger,
		serverContainer: declarionContainer,
		stopFn: func() {
			runCleanupStep(cfg.logger, "stop declarion container", func(ctx context.Context) error {
				return declarionContainer.Terminate(ctx)
			})
			runCleanupStep(cfg.logger, "stop postgres container", func(ctx context.Context) error {
				return pgContainer.Terminate(ctx)
			})
			runCleanupStep(cfg.logger, "remove test network", net.Remove)
			cleanupModuleBundle()
		},
	}

	return env, nil
}

const (
	systemTenantID   = "00000000-0000-0000-0000-000000000001"
	systemTenantCode = "test"
	systemUserID     = "00000000-0000-0000-0000-000000000002"

	// globalTenantID/globalTenantCode are the platform's `_global` sentinel
	// tenant (declarion-core internal/auth.GlobalTenantUUID). Entity write
	// floors gated `access: superadmin` require the caller to stand here.
	globalTenantID   = "00000000-0000-0000-0000-000000000000"
	globalTenantCode = "_global"

	containerCleanupTimeout = 30 * time.Second
)

func runCleanupStep(logger *zap.Logger, label string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		logger.Warn(label, zap.Error(err))
	}
}

// platformOperatorUserID/Email/DisplayName identify the SDK's synthetic
// platform-operator test actor - a REAL, DB-backed user, not merely a forged
// JWT claim. It reuses systemUserID so existing consumer code that references
// that fixed UUID (e.g. via testsdk.WithGlobalTenant) keeps working.
const (
	platformOperatorEmail       = "testsdk-platform-operator@declarion.local"
	platformOperatorDisplayName = "TestSDK Platform Operator"
)

// bootstrapTestPlatformActor creates, via raw SQL against the test Postgres
// container, the initial "test" tenant AND a real platform-operator test
// actor: a `users` row, `_global` tenant ownership, the `platform_admin`
// role, and its `user_roles` grant.
//
// Why the actor must be REAL, not just a forged JWT claim: declarion-core's
// tenant-scoped authority model (v0.29.0+) re-derives authority LIVE from the
// database for any async job drain (internal/auth/service_principal.go
// LoadPrincipal -> canAccessTenant/isSuperadminUser), ignoring the JWT claims
// that minted the enqueuing request entirely. A consumer test that creates a
// second tenant (which schedules an auto-seed job) or otherwise triggers an
// async handler as this actor would dead-letter with "user is not a member
// of tenant" if the actor has no real DB-backed `_global` ownership. This
// mirrors declarion-core's own real implementation of platform-operator
// identity (internal/auth/platform_operator.go grantPlatformOperatorTx and
// migration 097_superadmin_global_backfill.up.sql) exactly - same table
// shapes, same constraint names, same `platform_admin` / `["*"]` role - so it
// stays correct as core's schema evolves under normal migration discipline.
//
// This runs AFTER core's own migrator + seeder one-shot containers (so the
// `_global` tenant and the `platform_admin` role already exist per migration
// 097's unconditional backfill) and after the API container is confirmed
// healthy - the same point the pre-existing tenant-only bootstrap already
// ran at, before any test code executes. Raw SQL bypasses the API's own
// entity-write validation/audit, which is an accepted fixture-boundary
// trade-off for a one-time, pre-test bootstrap identity with no prior
// sessions - not a pattern for consumer test code to reach for elsewhere.
func bootstrapTestPlatformActor(ctx context.Context, pgContainer *postgres.PostgresContainer, net *testcontainers.DockerNetwork) error {
	sql := fmt.Sprintf(`
		BEGIN;

		INSERT INTO declarion.tenants (id, code, name)
		VALUES ('%[1]s', '%[2]s', '{"en":"System Test Tenant"}')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO declarion.users (id, email, display_name, kind, is_active, is_superadmin)
		VALUES ('%[3]s', '%[4]s', '%[5]s', 'person', true, true)
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO declarion.tenant_users (tenant_id, user_id, is_tenant_owner, created_by)
		VALUES ('%[6]s', '%[3]s', true, '%[3]s')
		ON CONFLICT ON CONSTRAINT tenant_user_unique
		DO UPDATE SET is_tenant_owner = true;

		INSERT INTO declarion.roles (tenant_id, code, name, permissions, created_by)
		VALUES ('%[6]s', 'platform_admin', 'Platform Admin', '["*"]'::jsonb, '%[3]s')
		ON CONFLICT ON CONSTRAINT roles_tenant_code_unique
		DO UPDATE SET code = EXCLUDED.code;

		INSERT INTO declarion.user_roles (tenant_user_id, role_id)
		SELECT tu.id, r.id
		FROM declarion.tenant_users tu, declarion.roles r
		WHERE tu.user_id = '%[3]s' AND tu.tenant_id = '%[6]s'
		  AND r.tenant_id = '%[6]s' AND r.code = 'platform_admin'
		ON CONFLICT ON CONSTRAINT user_role_unique DO NOTHING;

		COMMIT;
	`, systemTenantID, systemTenantCode, systemUserID, platformOperatorEmail, platformOperatorDisplayName, globalTenantID)

	exitCode, output, err := pgContainer.Exec(ctx, []string{
		"psql", "-U", "declarion", "-d", "declarion", "-v", "ON_ERROR_STOP=1", "-c", sql,
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

const (
	hostDirMode             os.FileMode = 0o755
	hostFileMode            os.FileMode = 0o644
	containerModuleTreeMode int64       = 0o755
)

var moduleNamePattern = regexp.MustCompile(`^_?[a-z][a-z0-9-]*$`)

// moduleBundle is the consumer module root copied into each Declarion container.
type moduleBundle struct {
	sourceDir  string // absolute path to <moduleName>/ containing manifest.yaml
	moduleName string
	dependsOn  []string // manifest depends_on, drives the DECLARION_MODULES list
	cleanup    func()
}

// buildModuleBundle resolves or stages the consumer module root. The returned
// directory is copied into /app/modules; no host bind mounts are used.
func buildModuleBundle(cfg *config) (*moduleBundle, error) {
	if strings.TrimSpace(cfg.moduleDir) != "" {
		if cfg.schemaDir != "" || cfg.migrationsDir != "" {
			return nil, fmt.Errorf("WithModuleDir cannot be combined with WithSchema or WithMigrations")
		}
		return buildModuleDirBundle(cfg)
	}
	if cfg.schemaDir == "" && cfg.migrationsDir == "" {
		return nil, nil
	}
	return buildSyntheticModuleBundle(cfg)
}

func buildModuleDirBundle(cfg *config) (*moduleBundle, error) {
	abs, err := filepath.Abs(cfg.moduleDir)
	if err != nil {
		return nil, fmt.Errorf("resolve module dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat module dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("module dir %q is not a directory", abs)
	}

	manifestName, dependsOn, err := readModuleManifest(abs)
	if err != nil {
		return nil, err
	}
	if err := validateModuleName(manifestName); err != nil {
		return nil, fmt.Errorf("manifest module name: %w", err)
	}
	if cfg.moduleNameSet && cfg.moduleName != manifestName {
		return nil, fmt.Errorf("WithModuleName(%q) does not match %s name %q", cfg.moduleName, filepath.Join(abs, "manifest.yaml"), manifestName)
	}
	if !cfg.moduleNameSet {
		cfg.moduleName = manifestName
	}
	if filepath.Base(abs) != cfg.moduleName {
		return nil, fmt.Errorf("module dir basename %q does not match module name %q", filepath.Base(abs), cfg.moduleName)
	}
	return &moduleBundle{sourceDir: abs, moduleName: cfg.moduleName, dependsOn: dependsOn, cleanup: func() {}}, nil
}

func buildSyntheticModuleBundle(cfg *config) (*moduleBundle, error) {
	if err := validateModuleName(cfg.moduleName); err != nil {
		return nil, fmt.Errorf("module name: %w", err)
	}
	tmpRoot, err := os.MkdirTemp("", "testsdk-module-*")
	if err != nil {
		return nil, fmt.Errorf("create temp module root: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpRoot) }

	moduleRoot := filepath.Join(tmpRoot, cfg.moduleName)
	if err := os.MkdirAll(moduleRoot, hostDirMode); err != nil {
		cleanup()
		return nil, fmt.Errorf("create module dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	manifest := fmt.Sprintf(
		"name: %s\nkind: application\nversion: \"0.0.0-test\"\nrevision: \"test\"\nbuild_time: \"%s\"\nschema_dir: schema\nmigrations_dir: migrations\n",
		cfg.moduleName, now,
	)
	if err := os.WriteFile(filepath.Join(moduleRoot, "manifest.yaml"), []byte(manifest), hostFileMode); err != nil {
		cleanup()
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	if cfg.schemaDir != "" {
		if err := copyDir(filepath.Join(moduleRoot, "schema"), cfg.schemaDir); err != nil {
			cleanup()
			return nil, fmt.Errorf("copy schema dir: %w", err)
		}
	}
	if cfg.migrationsDir != "" {
		if err := copyDir(filepath.Join(moduleRoot, "migrations"), cfg.migrationsDir); err != nil {
			cleanup()
			return nil, fmt.Errorf("copy migrations dir: %w", err)
		}
	}

	return &moduleBundle{sourceDir: moduleRoot, moduleName: cfg.moduleName, cleanup: cleanup}, nil
}

func attachModuleBundleFiles(req *testcontainers.GenericContainerRequest, b *moduleBundle) {
	if req == nil || b == nil {
		return
	}
	req.Files = append(req.Files, testcontainers.ContainerFile{
		HostFilePath:      b.sourceDir,
		ContainerFilePath: fmt.Sprintf("/app/modules/%s", b.moduleName),
		FileMode:          containerModuleTreeMode,
	})
}

func readModuleManifest(moduleDir string) (name string, dependsOn []string, err error) {
	manifestPath := filepath.Join(moduleDir, "manifest.yaml")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	var manifest struct {
		Name      string   `yaml:"name"`
		DependsOn []string `yaml:"depends_on"`
	}
	if err := yaml.Unmarshal(b, &manifest); err != nil {
		return "", nil, fmt.Errorf("parse manifest %s: %w", manifestPath, err)
	}
	return manifest.Name, manifest.DependsOn, nil
}

// platformBaseModule is the renamed platform module (declarion-core >= 0.40.1,
// formerly "_platform"): the base every consumer builds on, carrying the
// auth/tenant/user schema.
const platformBaseModule = "declarion-core"

// buildModuleSelector renders the DECLARION_MODULES value for a consumer's
// containers from the module the harness is starting: the consumer's declared
// depends_on (in manifest order) followed by the consumer module itself.
// declarion-core is prepended when the manifest does not already list it (e.g. a
// synthetic fixture module with no depends_on) so the platform schema is always
// present. Core validates that every depends_on entry appears EARLIER in the
// list (internal/modules/manifest.go), so a manifest's depends_on MUST be in
// load order - which it is by convention. A caller may still override the whole
// value via WithContainerEnv("DECLARION_MODULES", ...).
func buildModuleSelector(dependsOn []string, moduleName string) string {
	mods := make([]string, 0, len(dependsOn)+2)
	seen := make(map[string]bool)
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		mods = append(mods, m)
	}
	if !seen[platformBaseModule] {
		hasBase := false
		for _, d := range dependsOn {
			if strings.TrimSpace(d) == platformBaseModule {
				hasBase = true
				break
			}
		}
		if !hasBase {
			add(platformBaseModule)
		}
	}
	for _, d := range dependsOn {
		add(d)
	}
	add(moduleName)
	return strings.Join(mods, ",")
}

func validateModuleName(name string) error {
	if !moduleNamePattern.MatchString(name) {
		return fmt.Errorf("%q is invalid (must match %s)", name, moduleNamePattern)
	}
	return nil
}

func copyDir(dst, src string) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", src, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat %s: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", abs)
	}
	if err := os.CopyFS(dst, os.DirFS(abs)); err != nil {
		return fmt.Errorf("%s -> %s: %w", abs, dst, err)
	}
	return nil
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
