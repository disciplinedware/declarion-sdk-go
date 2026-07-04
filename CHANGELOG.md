# Changelog

All notable changes to `declarion-sdk-go` will be documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- `handlerparam` provides the shared minimal handler parameter declaration and reflection used by the SDK generator and declarion-core.
- `dispatch` provides the SDK-side typed handler registry and `Execute` kernel.
- `runtime.HandlerCtx.EntityCode` exposes the platform-provided `_entity_code` metadata beside `ObjectIDs`.

### Changed

- **BREAKING.** `runtime.RegisterFunction[P, R](method, fn, opts...)` is replaced by `runtime.RegisterHandler[P, R](code, fn)`. Registration no longer accepts options; actions, display, permissions, timeout, retry, webhook security, and other configuration live in YAML.
- **BREAKING.** `runtime.Ctx` is renamed to `runtime.HandlerCtx`.
- **BREAKING.** `runtime.RunGenerator()` is renamed to `runtime.Generate()`.
- The generator now emits only thin JSON-RPC handler stubs: handler code, `type: jsonrpc`, URL, and reflected params. It no longer emits `actions:` or rich handler metadata.

### Removed

- Removed the SDK mirror of core handler/action metadata: `HandlerMetadata`, `ActionMetadata`, `RetryConfig`, and all registration option constructors.

## [v0.14.0] - 2026-07-02

### Added

- **`testsdk.WithGlobalTenant`.** Mints the test context standing in the `_global`
  sentinel tenant instead of the bootstrapped system tenant. Required for writes
  to `access: superadmin` platform entities (`user`, `tenant`) since
  declarion-core's tenant-scoped authority model gates those on standing in
  `_global`, not merely an `IsSuperadmin` claim.

### Fixed

- `testsdk.NewCtx`'s `WithTenant` previously changed only the JWT's tenant CODE
  claim; the tenant ID claim (and the returned `ctx.TenantID`) stayed hardcoded
  to the system test tenant regardless. `NewCtx` now honors the resolved tenant
  ID consistently, which `WithGlobalTenant` depends on.
- **`StartPlatform`'s bootstrap actor is now a real, DB-backed platform
  operator**, not merely a forged JWT claim. `bootstrapTenant` (tenant-row-only)
  is replaced by `bootstrapTestPlatformActor`, which additionally creates the
  `users` row, real `_global` tenant ownership, and the `platform_admin` role -
  mirroring declarion-core's own `internal/auth/platform_operator.go`
  `grantPlatformOperatorTx` exactly. Needed because the tenant-scoped authority
  model re-derives authority LIVE from the database for async job drain and
  does not trust the enqueuing request's JWT claims; a forged-only actor
  dead-lettered with "user is not a member of tenant" the first time a test
  triggered an async job (e.g. `tenant.__create`'s auto-seed).

## [v0.13.1] - 2026-06-26

### Fixed

- Version-stamped the v0.13.0 release notes in this changelog. No code changes
  from v0.13.0.

## [v0.13.0] - 2026-06-26

### Added

- **`testsdk.WithModuleDir`.** Consumer integration tests can now copy a real
  Declarion module root into the test platform container. The directory basename
  and `manifest.yaml:name` must match, so tests exercise the same module layout
  production images ship.
- **`platform.CountMode`.** `ListParams.Count` now maps to the platform
  `count=with|only|exact` query contract.
- **`platform.BatchOp.ObjectIDs` and `Batch.AddOp`.** Batch operations now carry
  `object_ids` at the operation top level, matching `system.batch`.

### Changed

- **testsdk module loading no longer uses Docker bind mounts.** `StartPlatform`
  copies module trees into the Declarion containers through testcontainers'
  file-copy API, which avoids host permission leaks and works better with
  non-root images, Windows hosts, and remote Docker.

### Removed

- **BREAKING.** `platform.ListParams.IncludeCount` was replaced by
  `ListParams.Count`. Use `platform.CountWith` for the old rows-plus-total
  behavior.

## [v0.12.0] - 2026-06-17

### Added

- **`GlobalOnly()` and `Internal()` registration options.** Two declarion-core
  handler/action flags the YAML generator could not emit before. `GlobalOnly()`
  marks a handler that may run only in the `_global` tenant (cross-tenant
  cleanup, platform self-diagnostics) and emits `global_only: true`.
  `Internal()` marks a scheduler-only action - callable from
  `declarion.schedules` but not over HTTP, and hidden from action lists - and
  emits `internal: true`. Derivative projects can now declare these handlers in
  Go instead of hand-writing the YAML.

## [v0.11.4] - 2026-06-04

### Fixed

- **testsdk: seed the platform `bootstrap` profile before the API container boots.**
  declarion-core 0.5.0's `api` role reconciles the per-tenant `tenant_bootstrap`
  profile at startup, and that profile's platform-scheduler membership `$lookup`s the
  global `platform-scheduler@declarion.local` technical user created by the
  `bootstrap` profile. `StartPlatform` previously ran migrate-only then booted `api`,
  so the user never existed and the container died with
  `reconcile tenant_bootstrap (boot): ... got 0 rows`. A new seed one-shot now runs
  `declarion seed apply` (scoped to `DECLARION_MODULES=_platform`,
  `DECLARION_SEED_PROFILES=bootstrap`) between migrate and api, mirroring the
  production migrator -> seeder -> api order. The seed and api containers share one
  `DECLARION_SECRET_KEYS` set (the seed step boots the full app, which fail-closes on
  empty secret keys). Consumer-agnostic: it never touches a consumer's own bootstrap
  bundle.

## [v0.11.3] - 2026-06-03

### Fixed

- **testsdk: set `DECLARION_MODULES` on the spawned containers.** declarion-core
  ≥ 0.4.7 fail-closes when `DECLARION_MODULES` is unset (it used to activate
  every discovered module). `StartPlatform` now derives
  `DECLARION_MODULES=_platform,<WithModuleName>` and sets it on both the migrate
  one-shot and the API server container, so consumer integration tests keep
  booting against fail-closed core. A caller may override via `WithContainerEnv`.

## [v0.11.0] - 2026-06-01

### Removed

- **BREAKING.** `runtime.Schedulable()` option (added in v0.10.1) is
  DELETED along with `HandlerMetadata.Schedulable` field. The
  declarion-core gate that consulted this flag has been removed; any
  action whose handler resolves is now schedulable. Consumers who
  added `sdk.Schedulable()` to handler registrations MUST remove the
  call — it no longer compiles.

### Notes

v0.10.1 had a ~1-hour lifespan; no production consumers shipped against
it. v0.11.0 supersedes.

## [v0.10.1] - 2026-06-01

### Added

- `runtime.Schedulable()` option — opts a handler into dispatch from
  declarion-core's scheduler (declarion.schedules table) and future
  periodic-job sources. Without this flag the scheduler refuses to
  enqueue the handler at the gate, even if an action wrapping it is
  referenced by a schedule row. Defense-in-depth: prevents arbitrary
  handlers from being scheduled via direct ABAC write or out-of-band
  SQL. Required when a consumer-registered handler is intended to be
  reachable from declarion.schedules.
- `HandlerMetadata.Schedulable` field stores the flag; emitted as
  `schedulable: true` in generated YAML.

## [v0.10.0] - 2026-06-01

### Added

- `runtime.RunGenerator()` — canonical one-liner for a derivative project's
  `cmd/gen-functions-yaml` binary. Writes `runtime.GeneratedHeader` followed
  by `GenerateFunctionsYAML(os.Stdout)`, exits non-zero on any error. Lets
  per-project generator binaries shrink to a literal `func main() {
  sdk.RunGenerator() }`.
- `runtime.GeneratedHeader` — exported constant containing the canonical
  "do not edit, regenerate via make gen-functions-yaml" comment block
  prepended to generated YAML files.
- `examples/gen-functions-yaml/main.go` — canonical template for the
  per-project generator binary. Copy verbatim into derivative projects,
  edit only the blank-import block.
- `examples/Makefile.snippet` — canonical Makefile target pair
  (`gen-functions-yaml` + `verify-functions-yaml`) with identical
  target names, output path, and atomic-write pattern across every
  derivative project. Copy verbatim.

### Migration

No breaking changes; v0.9.0 callers continue to work. Existing
`cmd/gen-functions-yaml/main.go` binaries can optionally shrink to the
template shape by replacing their hand-written header + GenerateFunctionsYAML
call with `sdk.RunGenerator()`.

## [v0.9.0] - 2026-06-01

### Changed

- **BREAKING.** `runtime.Handler[P, R]()` builder + `runtime.RegisterHandler(h)`
  + `runtime.GenerateHandlersYAML(out)` are REPLACED by a single primitive
  `runtime.RegisterFunction[P, R](method, fn, opts...)` and a single
  generator `runtime.GenerateFunctionsYAML(out)`. `runtime.HandlerOption`
  is REPLACED by `runtime.Option`. `runtime.HandlerRegistration` is now
  internal (`registration`).
- Generator emits `handlers:` + `actions:` blocks in one document. UDFs
  (registrations created with `NoAction()`) appear only in the `handlers:`
  block. Functions with any group-C UI option (NameEN/NameRU/Icon/
  Destructive/LongRunning/ProgressScreen) — or an explicit `Action()` —
  also appear in `actions:`.
- `runtime.HandleRPCForTest(w, r, cfg)` no longer takes a registry map
  parameter; it builds the registry from the package-level handlerRegistry
  populated by RegisterFunction.
- Adds Option constructors covering action UI metadata (NameEN, NameRU,
  Icon, Destructive, LongRunning, ProgressScreen, Action, NoAction,
  RequiredPermission) and handler dispatch fields (Idempotent, Invoke,
  AllowNoObjects, ReadOnly, SuppressEvents, Audit, RequestVerifier) on
  top of the prior surface (Timeout, Async, Retry, Unauthenticated,
  RawBodyAccess, MaxBodyBytes, RequestDedupKeyExpr, RequestDedupKeyParam,
  TenantFromHeader, TenantFromPayloadLookup).

### Migration

Every call site of the old API requires manual migration:

1. Replace `RegisterHandler(Handler[P, R](method, fn, opts...))` with
   `RegisterFunction[P, R](method, fn, opts...)`.
2. If the function is meant as a pure handler with no action wrapper
   (a pure-compute UDF with no permission gate or UI exposure), add
   `NoAction()` to the opts.
3. If the function needs an action wrapper but lacks UI metadata, add
   `Action()` explicitly.
4. Update generator binary + YAML filename + Makefile targets from
   `GenerateHandlersYAML` / `actions.generated.yaml` / `gen-handlers-yaml`
   to `GenerateFunctionsYAML` / `_functions.generated.yaml` /
   `gen-functions-yaml` (and `verify-functions-yaml`).
5. Update `runtime.HandleRPCForTest` call sites to drop the registry-map
   parameter; register handlers via RegisterFunction before invocation.

No backward-compat aliases. v0.8.x and earlier consumers MUST migrate.

## [v0.7.0] - 2026-06-01

### Changed

- **BREAKING.** `runtime.Serve(cfg, handlers...)` is now `runtime.Serve(cfg)`.
  Serve walks the package-level `handlerRegistry` populated by
  `runtime.RegisterHandler` — same registry that `GenerateHandlersYAML` reads.
  Single source of truth for both the runtime dispatch table and the YAML
  manifest. Eliminates the historical split where `Serve` and the generator
  operated on disjoint sources.

### Migration

Callers passing handlers as varargs MUST migrate by:

1. Ensure every handler is registered via `runtime.RegisterHandler(...)` in
   an `init()` of its handler package (project-specific wrappers typically
   delegate to RegisterHandler).
2. Drop the varargs from the `Serve` call: `runtime.Serve(cfg)`.

Tests that need ad-hoc handler tables can keep using `handleRPC` directly
via a project-built mux (see runtime/testing.go); they were already
independent of `Serve`.

## [v0.5.0] - 2026-05-23

### Changed

- **BREAKING (wire contract).** `platform.DataClient.Update` now POSTs to
  `/api/actions/{entity}.__update` (request body `{entity, items}`, response
  envelope `{status, result: {rows, rows_matched}, audit_operation_id}`) instead
  of the retired `PATCH /api/data/{entity}` CRUD route. Method signature is
  unchanged - existing callers compile - but the SDK now requires
  declarion-core `>= 0.4.x` (unified-action-toolbar migration; PATCH route is
  gone server-side). Routes through the action dispatcher so permission
  gating, ABAC, audit, and `$on_before_update` / `$on_updated` lifecycle
  events flow through the canonical chokepoint.

### Fixed

- **`platform.DataClient.Delete` is no longer a 410-Gone live bug.** It now
  POSTs to `/api/actions/{entity}.__delete` with `{"_ids": [...]}` (the
  platform default delete action) instead of the retired
  `POST /api/data/{entity}/delete` CRUD route. Single-column PK values pass
  through verbatim. Composite-PK callers must pre-encode their object IDs;
  attempting to delete with a multi-field PK map now errors loudly rather
  than silently emitting a randomly-ordered object ID that would mismatch
  `store.SplitObjectID` server-side.

### Compatibility

Wire-contract changes against declarion-core. External consumers pinned to
the previous Update/Delete behavior must upgrade declarion-core in lock-step
with this SDK release. Plan to publish under a major-version bump when the
next tag cuts.

## [0.4.0] - 2026-05-07

### Added

- `runtime.HandlerOption` function type and five option constructors: `Timeout(d)`,
  `NameEN(s)`, `NameRU(s)`, `Retry(maxAttempts, backoff)`, `Async()`.
- `runtime.HandlerMetadata` struct stored on `HandlerRegistration.Metadata`; populated
  by options, read by the generator. No effect on `Serve` dispatch.
- `runtime.RetryConfig` struct (`MaxAttempts int`, `Backoff string`).
- `runtime.Handler[P, R]` now accepts `...HandlerOption` (backward-compatible; existing
  call sites pass no options and compile unchanged).
- `runtime.RegisterHandler(h HandlerRegistration)` adds a handler to the process-wide
  registry consumed by `GenerateHandlersYAML`. Call from `init()` in each handler package.
- `runtime.ClearHandlerRegistry()` test helper; clears the process-wide registry.
- `runtime.GenerateHandlersYAML(out io.Writer) error` walks the process-wide registry
  sorted alphabetically and emits a YAML `handlers:` block compatible with
  declarion-core's schema loader. Used by `cmd/gen-handlers-yaml` in consumers.

### Compatibility

Targets declarion-core `>= 0.1.4`. All v0.3.x consumers compile without changes.

## [0.1.0] - 2026-04-16

Initial tagged release. Prior development was unversioned (consumers pinned pseudo-versions).

### Packages
- `runtime` — out-of-process handler SDK (`sdk.Serve`, `sdk.Handler`). Consumers register JSON-RPC handlers invoked by the declarion-core platform.
- `platform` — client for calling back into declarion-core (`ctx.Platform.Data()` etc.).
- `conformance` — test fixtures and helpers for SDK/platform contract verification.
- `testsdk` — test doubles for consumer unit tests.

### Compatibility
- Targets declarion-core `>= 0.1.4`.

[Unreleased]: https://github.com/disciplinedware/declarion-sdk-go/compare/v0.13.1...HEAD
[v0.13.1]: https://github.com/disciplinedware/declarion-sdk-go/compare/v0.13.0...v0.13.1
[v0.13.0]: https://github.com/disciplinedware/declarion-sdk-go/compare/v0.12.2...v0.13.0
[0.1.0]: https://github.com/disciplinedware/declarion-sdk-go/releases/tag/v0.1.0
