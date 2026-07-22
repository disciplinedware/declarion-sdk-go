package testsdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

func TestBuildModuleBundle_WithModuleDirUsesRealModuleRoot(t *testing.T) {
	moduleDir := filepath.Join(t.TempDir(), "sample-module")
	writeTestFile(t, filepath.Join(moduleDir, "manifest.yaml"), `name: sample-module
kind: application
version: "dev"
revision: "dev"
build_time: "2020-01-01T00:00:00Z"
schema_dir: schema
migrations_dir: migrations
`)
	writeTestFile(t, filepath.Join(moduleDir, "schema", "entities.yaml"), "entities: []\n")
	writeTestFile(t, filepath.Join(moduleDir, "migrations", "001_init.up.sql"), "SELECT 1;\n")

	cfg := &config{moduleDir: moduleDir, moduleName: "test-consumer"}
	bundle, err := buildModuleBundle(cfg)
	if err != nil {
		t.Fatalf("buildModuleBundle: %v", err)
	}
	defer bundle.cleanup()

	abs, err := filepath.Abs(moduleDir)
	if err != nil {
		t.Fatalf("abs moduleDir: %v", err)
	}
	if bundle.sourceDir != abs {
		t.Fatalf("sourceDir: got %q, want %q", bundle.sourceDir, abs)
	}
	if bundle.moduleName != "sample-module" || cfg.moduleName != "sample-module" {
		t.Fatalf("module name: bundle=%q cfg=%q, want sample-module", bundle.moduleName, cfg.moduleName)
	}

	var req testcontainers.GenericContainerRequest
	attachModuleBundleFiles(&req, bundle)
	if req.HostConfigModifier != nil {
		t.Fatal("module bundle must not install Docker bind mounts")
	}
	if len(req.Files) != 1 {
		t.Fatalf("files: got %d, want 1", len(req.Files))
	}
	if req.Files[0].HostFilePath != abs {
		t.Fatalf("host file path: got %q, want %q", req.Files[0].HostFilePath, abs)
	}
	if req.Files[0].ContainerFilePath != "/app/modules/sample-module" {
		t.Fatalf("container path: got %q, want /app/modules/sample-module", req.Files[0].ContainerFilePath)
	}
}

func TestBuildModuleBundle_WithSchemaMigrationsStagesSyntheticModule(t *testing.T) {
	root := t.TempDir()
	schemaDir := filepath.Join(root, "schema")
	migrationsDir := filepath.Join(root, "migrations")
	writeTestFile(t, filepath.Join(schemaDir, "entities.yaml"), "entities: []\n")
	writeTestFile(t, filepath.Join(migrationsDir, "001_init.up.sql"), "SELECT 1;\n")

	cfg := &config{
		moduleName:    "test-consumer",
		schemaDir:     schemaDir,
		migrationsDir: migrationsDir,
	}
	bundle, err := buildModuleBundle(cfg)
	if err != nil {
		t.Fatalf("buildModuleBundle: %v", err)
	}
	sourceDir := bundle.sourceDir
	defer bundle.cleanup()

	for _, rel := range []string{
		"manifest.yaml",
		filepath.Join("schema", "entities.yaml"),
		filepath.Join("migrations", "001_init.up.sql"),
	} {
		if _, err := os.Stat(filepath.Join(sourceDir, rel)); err != nil {
			t.Fatalf("staged %s: %v", rel, err)
		}
	}

	manifest, err := os.ReadFile(filepath.Join(sourceDir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read staged manifest: %v", err)
	}
	for _, want := range []string{"name: test-consumer", "schema_dir: schema", "migrations_dir: migrations"} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("manifest missing %q:\n%s", want, string(manifest))
		}
	}

	var req testcontainers.GenericContainerRequest
	attachModuleBundleFiles(&req, bundle)
	if req.HostConfigModifier != nil {
		t.Fatal("synthetic module bundle must not install Docker bind mounts")
	}
	if len(req.Files) != 1 {
		t.Fatalf("files: got %d, want 1", len(req.Files))
	}
	if req.Files[0].ContainerFilePath != "/app/modules/test-consumer" {
		t.Fatalf("container path: got %q, want /app/modules/test-consumer", req.Files[0].ContainerFilePath)
	}

	bundle.cleanup()
	if _, err := os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove staged module dir, stat err=%v", err)
	}
}

func TestBuildModuleBundle_RejectsAmbiguousOrMismatchedModules(t *testing.T) {
	t.Run("module_dir_with_schema", func(t *testing.T) {
		_, err := buildModuleBundle(&config{
			moduleDir: t.TempDir(),
			schemaDir: t.TempDir(),
		})
		if err == nil || !strings.Contains(err.Error(), "WithModuleDir cannot be combined") {
			t.Fatalf("error = %v, want WithModuleDir combination rejection", err)
		}
	})

	t.Run("explicit_module_name_mismatch", func(t *testing.T) {
		moduleDir := filepath.Join(t.TempDir(), "sample-module")
		writeTestFile(t, filepath.Join(moduleDir, "manifest.yaml"), "name: sample-module\nkind: application\n")
		_, err := buildModuleBundle(&config{
			moduleDir:     moduleDir,
			moduleName:    "other",
			moduleNameSet: true,
		})
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v, want module name mismatch", err)
		}
	})

	t.Run("manifest_name_must_match_directory", func(t *testing.T) {
		moduleDir := filepath.Join(t.TempDir(), "sample-module")
		writeTestFile(t, filepath.Join(moduleDir, "manifest.yaml"), "name: other\nkind: application\n")
		_, err := buildModuleBundle(&config{moduleDir: moduleDir})
		if err == nil || !strings.Contains(err.Error(), "basename") {
			t.Fatalf("error = %v, want module dir basename mismatch", err)
		}
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), hostDirMode); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), hostFileMode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBuildModuleSelector(t *testing.T) {
	cases := []struct {
		name       string
		dependsOn  []string
		moduleName string
		want       string
	}{
		{
			// The real consumer shape (declarion-crm): manifest depends_on already
			// lists the platform base first, so the whole declared set + the
			// consumer module is rendered in order.
			name:       "consumer_with_full_depends_on",
			dependsOn:  []string{"declarion-core", "agents"},
			moduleName: "declarion-crm",
			want:       "declarion-core,agents,declarion-crm",
		},
		{
			// Synthetic fixture module (no depends_on): the platform base is
			// prepended so the auth/tenant schema is present.
			name:       "synthetic_no_depends_on",
			dependsOn:  nil,
			moduleName: "test-consumer",
			want:       "declarion-core,test-consumer",
		},
		{
			// A manifest that omits the base still gets it prepended (fail-safe).
			name:       "depends_on_without_base",
			dependsOn:  []string{"agents"},
			moduleName: "widget",
			want:       "declarion-core,agents,widget",
		},
		{
			// No duplication when the base and a dep repeat.
			name:       "dedup",
			dependsOn:  []string{"declarion-core", "declarion-core", "agents"},
			moduleName: "widget",
			want:       "declarion-core,agents,widget",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildModuleSelector(tc.dependsOn, tc.moduleName); got != tc.want {
				t.Fatalf("buildModuleSelector(%v, %q) = %q, want %q", tc.dependsOn, tc.moduleName, got, tc.want)
			}
		})
	}
}
