package testsdk

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
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
	pgContainer, err := postgres.Run(ctx, "postgres:17-alpine",
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

	// Run migrations first via a one-shot container.
	migrateReq := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: cfg.image,
			Env: map[string]string{
				"DECLARION_DATABASE_URL": dbURL,
				"DECLARION_MODULES_DIR":  "/app/modules",
			},
			Cmd:        []string{"./declarion", "migrate", "apply"},
			WaitingFor: wait.ForExit().WithExitTimeout(60 * time.Second),
		},
		Started: true,
	}
	if mm != nil {
		migrateReq.ContainerRequest.Mounts = mm.mounts()
	}
	network.WithNetwork([]string{"test-migrate"}, net)(&migrateReq)

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
		cfg.logger.Info("migration completed", "logs", string(logBytes))
	}
	_ = migrateContainer.Terminate(ctx)

	// Start the Declarion API server.
	serverEnv := map[string]string{
		"DECLARION_DATABASE_URL": dbURL,
		"DECLARION_JWT_SECRET":   cfg.jwtSecret,
		"DECLARION_ROLES":        "api",
		"DECLARION_MODULES_DIR":  "/app/modules",
	}
	for k, v := range cfg.containerEnv {
		serverEnv[k] = v
	}
	declarionReq := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: cfg.image,
			Env:   serverEnv,
			ExposedPorts: []string{"3000/tcp"},
			WaitingFor: wait.ForHTTP("/api/health").
				WithPort("3000/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	}
	if mm != nil {
		declarionReq.ContainerRequest.Mounts = mm.mounts()
	}
	network.WithNetwork([]string{"test-declarion"}, net)(&declarionReq)

	declarionContainer, err := testcontainers.GenericContainer(ctx, declarionReq)
	if err != nil {
		cleanupModuleDir()
		_ = pgContainer.Terminate(ctx)
		_ = net.Remove(ctx)
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

	cfg.logger.Info("platform started", "url", url)

	env := &PlatformEnv{
		URL:              url,
		JWTSecret:        cfg.jwtSecret,
		logger:           cfg.logger,
		serverContainer:  declarionContainer,
		stopFn: func() {
			termCtx := context.Background()
			if err := declarionContainer.Terminate(termCtx); err != nil {
				slog.Warn("stop declarion container", "error", err)
			}
			if err := pgContainer.Terminate(termCtx); err != nil {
				slog.Warn("stop postgres container", "error", err)
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
		"name: %s\nkind: consumer\nversion: \"0.0.0-test\"\nrevision: \"test\"\nbuild_time: \"%s\"\n",
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

// mounts returns the bind mounts for a container. Each real directory gets its
// own mount (no symlinks - Docker bind mounts don't follow host-side symlinks).
func (m *moduleMount) mounts() testcontainers.ContainerMounts {
	base := fmt.Sprintf("/app/modules/%s", m.moduleName)
	result := testcontainers.ContainerMounts{
		// Manifest lives in the temp dir.
		testcontainers.BindMount(m.manifestDir+"/manifest.yaml", testcontainers.ContainerMountTarget(base+"/manifest.yaml")),
	}
	if m.schemaDir != "" {
		result = append(result, testcontainers.BindMount(m.schemaDir, testcontainers.ContainerMountTarget(base+"/schema")))
	}
	if m.migrDir != "" {
		result = append(result, testcontainers.BindMount(m.migrDir, testcontainers.ContainerMountTarget(base+"/migrations")))
	}
	return result
}
