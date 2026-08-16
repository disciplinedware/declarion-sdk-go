# Changelog

All notable changes to `declarion-sdk-go` will be documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **`errs` - one error object, on RFC 9457, for every Declarion path.** `errs.Error` is the wire shape a failure serializes to on every carrier: `type` is the identifier, `status` is advisory and absent where no HTTP status exists, `retryable` is always present and answered from the declaration rather than computed from a status, and declared fields are top-level members. `errs.New(code, Args{...})` raises one, `Because` attaches the operator's cause (unexported, unmarshalable by construction, never on a wire), and `errs.From` walks a wrapped chain. `errs.Render` is the ONE rendering rule both the platform and an application answering its own client call: it takes `status`, `retryable` and the localized `title` from the loaded catalogue and discards whatever a producer put in `status`, `title` and `instance`. A RAISED occurrence carries none of those three - they describe a boundary it has not reached - which is what lets the same object be stored in a row without freezing one caller's language or an HTTP status a background job never had. `errs.Bounded` is the one validate-or-replace against `errs.DefaultMaxBytes`. `errs.TypeDef` / `errs.Catalogue` are the declaration shape a module's `errors:` block decodes into, so one type serves the decode and the render. Nothing here parses schema YAML or loads a catalogue.
- **`errs/errscheck` - the call-site gate.** A code is a plain string written the way the YAML declares it, so the compiler cannot catch a typo. `errscheck.Check` reads the AST and refuses a call site naming an undeclared code, misspelling it, passing more than one `Args`, or passing a member the type does not declare. An empty catalogue fails the gate rather than skipping it. Test files are out of scope: a test proving the undeclared-type fallback has to raise one.

- **`*errs.Error` is nil-safe on the four methods that travel.** `Error`, `Unwrap`, `Is` and `Code` no longer panic on a nil receiver. This type is designed to travel as an `error`, and a nil pointer inside a non-nil interface is a shape `errors.Is` and `errors.As` walk into while looking for something else - so a caller holding a `*errs.Error` on a SUCCESS path and handing it over as `error` took the process down on a path that had nothing to do with this error. Found twice by real code: an audit flush and an export.

- **`HandlerCtx.Locale`** - the language the platform already resolved for this caller, from the reserved `_locale` param. A handler rendering its own text uses it instead of resolving a language again on the other side of the boundary and reaching a different answer; empty when the platform sent none, because "" is not a language. **Deployment order matters and is not optional**: the SDK fails closed on any unknown reserved key - the right posture, since a caller must not smuggle spoofed platform metadata past the typed param surface - so a sidecar built on an older SDK does not degrade when a newer core sends `_locale`, it rejects EVERY handler call with invalid-params. Ship this SDK to every sidecar BEFORE any core that emits it.

- **`ListParams.Expand`** - name the sub-resources a read should attach: `refs`, `statuses`, `properties`, `evidence`. The platform has always accepted `expand`; the SDK had no way to send it, so a row's declared object properties were unreachable through this client and a caller needing one had no read that could return it. Empty keeps today's behaviour exactly - display-level refs and nothing else. Composes with `Select`, because the platform's field trim never removes a `$`-prefixed sub-resource key.

## [v0.20.0] - 2026-08-11

### Changed

- **BREAKING: `platform.FilterNode` uses direct recursive logical children.** `Or` is now `[]FilterNode`, matching Declarion Core's final JSON/YAML tree; use `platform.Or(...)` and `platform.And(...)` for logical nodes. The removed OR-of-AND representation has no decoder or compatibility path.

## [v0.19.1] - 2026-08-07

### Documentation

- **`platform.FilterNode` says who owns the filter grammar, and names the value tokens.** The type carried the contract core has now deleted on its side - "Mirrors the server's allowlist exactly; changes require a coordinated SDK + server release" - which is a copy held together by a comment, and the comment was the only thing holding it. Go's `internal/` visibility rule means this package cannot import the server's declaration, so the copy is unavoidable; being unwatched was not. The constants are now documented as a convenience over the ONE authority (`engine.AllFilterOperators`), which core publishes at `GET /api/schema` under `filter_grammar` (`operators`, `no_value_ops`, `multi_value_ops`), and core carries a test that reads this file and fails when the two disagree in either direction. `Value` also documents the value tokens a filter may carry - `$user.id` / `$user.email` / `$user.tenant_id`, and `$now` with composable signed offsets (`$now+3w`, `$now+3w-1d`) - which a Go integrator could previously find only in the platform handbook. No behaviour change and no wire change.

## [v0.19.0] - 2026-08-03

### Fixed

- **The test harness mints the grant list, not just the flag.** `testsdk` minted its actor with `IsSuperadmin` and an EMPTY permission list, leaning on core's old `IsSuperadmin || IsTenantOwner || <grant match>` short-circuit. Core 0.44.0 resolves a caller from one materialised list and deliberately re-derives nothing from a flag, so that token became one no gate admits and every call in a consumer's integration tier answered 403. The actor now carries the wildcard IN the list, which is what a real resolver-issued owner token looks like; the flags stay beside it for the paths that read an ownership FACT rather than a grant.

### Added

- **`testsdk.WithTenantID(id)`.** Mints a context standing in a tenant the test created, instead of the bootstrapped system tenant. `WithTenant` sets the code alone and keeps the system tenant's id, so it cannot express a SECOND tenant - the shape a tenant-isolation proof needs. Consumers were hand-rolling the claim set to get it, which is how one change to what core admits reached each of them separately.

## [v0.18.0] - 2026-07-25

### Added

- **Streaming action client.** `ActionsClient.InvokeStreaming(ctx, code, params)` consumes a dual-mode action's request-scoped SSE response over the same `/api/actions/{code}` route (the buffered `ActionsClient.Invoke` covers stream=false). It returns an `*ActionStream` after validating the mandatory start event, then exposes immutable start `Meta()`, `Next()`/`Data()` over ordered raw frame bytes, terminal `Err()`, and an idempotent `Close()` that cancels the request. The parser holds one bounded event (`MaxStreamEventSize`, 100MB - matching the platform's `DefaultHandlerResponseLimit`) plus fixed state, enforces that cap against the running byte total on every internal read fragment (never buffering an unterminated line past the cap before checking), joins multiline `data:` with LF, ignores comment heartbeats, and fails closed on CR, invalid UTF-8, unknown control events, out-of-order events, oversize events, or EOF before the terminal event. Pre-start non-2xx surfaces as `*APIError`; a post-start terminal failure surfaces as `*StreamError`. The client never interprets frame payloads, so native provider SSE passes through unchanged. Requires declarion-core with streaming-action support. This generic buffered+streaming action client is the ONLY thing a consumer needs to call any action (including `llm_connector.invoke`); the SDK stays universal and carries no action-specific typed client.
- **`runtime.MintHandlerToken(jwtSecret, params)`.** Signs a handler-dispatch (continuation) token byte-compatible with declarion-core's `auth.HandlerTokenManager.Mint`, for the case where a trusted operator sidecar must originate a `callable_from:sidecar` call rather than ride an inbound Core dispatch. Takes a least-privilege `HandlerTokenParams` (acting user/tenant, baked `Permissions`, target `Action`, short `TTL`, authority bits) and signs HS256 with the shared platform secret - possession of the secret is the entire trust boundary, so callers must mint narrow, short-lived tokens for a verified subject only. `HandlerTokenIssuer` and `HandlerTokenGrace` are exported alongside the existing `HandlerTokenAudience`/`HandlerTokenScope`.

### Fixed

- **The default platform HTTP client no longer imposes a 60-second hard timeout — it relies on the request context.** Every request is issued via `http.NewRequestWithContext`, but `platform.New` defaulted to `&http.Client{Timeout: 60s}`, a HARD cap that silently overrides the caller's context deadline. This cut long-but-legitimate calls at 60s: an LLM-connector inference whose action declares a 10-minute server timeout (real `lead.parse` averages 85s) was aborted client-side at 60s while the server kept running the detached, already-charged request — the caller saw a transient error and could retry into a DOUBLE CHARGE. The default is now `&http.Client{}` (no client-level timeout); callers pass a bounded context (handler/operation deadline) and server-side action timeouts bound anything that would otherwise hang. A caller that needs a hard cap still sets `Config.HTTPClient` explicitly. `testsdk.NewCtx` now seeds the handler context with a deadline derived from `t.Deadline()` (the `-timeout` watchdog, with headroom, parented to `t.Context()`) instead of `context.Background()` — so a SYNCHRONOUS stalled test request fails on its own per-test context just before the watchdog, rather than blocking the test body (which prevents `t.Context()` from ever cancelling) and hanging the whole suite.

- **`testsdk` platform-actor bootstrap no longer writes the removed `users.is_superadmin` column.** declarion-core's auth-bootstrap redesign dropped `users.is_superadmin` — platform-superadmin authority is now `_global` ownership (`isSuperadminUser == isTenantOwner(_global)`) plus the `platform_admin` `["*"]` role. `bootstrapTestPlatformActor` still INSERTed `is_superadmin`, so every consumer's integration/e2e suite failed at boot (`column "is_superadmin" of relation "users" does not exist`) against core built from current source. The users INSERT now sets no superadmin flag; the actor's superadmin identity comes from the `_global` `tenant_users` owner row it already creates.

## [v0.17.2] - 2026-07-22

### Fixed

- **`testsdk.StartPlatform` derives `DECLARION_MODULES` from the consumer manifest's `depends_on` instead of the retired `_platform` module name.** declarion-core 0.40.1 renamed the platform base module `_platform` to `declarion-core` and fail-closes when `DECLARION_MODULES` names an unknown module OR omits any declared `depends_on` entry. The harness hardcoded `_platform,<consumer>` (and `_platform` for the bootstrap seeder), so every consumer's integration tests broke against core >= 0.40.1, and the two-element default could never satisfy a consumer that depends on more than the base (e.g. `declarion-crm` depends on `declarion-core` + `agents`). The module list is now built from the staged manifest's `depends_on` (in declared order) plus the consumer module, with `declarion-core` prepended when absent; the seeder scopes to `declarion-core`. A caller may still override the whole value via `WithContainerEnv("DECLARION_MODULES", ...)`. Covered by `TestBuildModuleSelector`.

## [v0.17.1] - 2026-07-22

## [v0.17.0] - 2026-07-14

### Added

- **Predicate-addressed delete.** `BulkDelete` takes a variadic `platform.DeleteWhere(filters...)` option carrying the same `FilterNode` grammar a list read uses, so rows can be addressed by predicate instead of (or in addition to) by id - passing both is a guard: "delete these ids, but only while they still match". `Batch.Delete` takes the same option. Variadic, so existing three-argument call sites compile unchanged. The client refuses to send a delete that addresses nothing. Requires declarion-core with filter-addressed `data.delete`.
- **`DataClient.WithTargetTenantID` / `WithTargetTenantCode`.** A data read or write can now target one tenant, the way `Batch` and the actions client already could - a cross-tenant service identity previously had to wrap a plain write in a batch just to reach the right tenant.

- **Verifiers for anonymous provider webhooks.** `runtime.RegisterVerifier` registers application code that authenticates a credential-less provider call (Stripe signature, Telegram secret token) before Declarion admits it. Served on the existing `/rpc` endpoint under its own powerless token audience, with a registry disjoint from handlers. `VerifierCtx` exposes the exact raw body, the named path values, and only the allowlisted headers/query; `VerifierCtx.Platform` is non-nil only when the declaration names a run-as service user, and is never built from the verifier's own call token.
- `runtime.Reject` / `runtime.InvalidRequest` / `runtime.Unavailable` - the typed verifier outcomes Declarion maps to one uniform public response each (401 / 400 / 503). A plain error is treated as unavailable, never as a rejection.
- `VerifierResult.TargetTenantID` and `.UserID` are optional: the verifier fills only what it alone can know, and the verifier declaration's `dispatch_tenant` / `dispatch_user` supply the rest. `.Params` carries trusted values that Declarion merges into the handler's params (they beat body fields).
- Handler tokens now carry a `Method` claim, bound at serve time, so a token minted for one method cannot invoke another.

## [v0.16.0] - 2026-07-08

### Added

- `testsdk.PlatformEnv.DBPool(t)` exposes a test-only pgx pool for assertions against the database migrated by `StartPlatform`.

## [v0.15.0] - 2026-07-05

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
