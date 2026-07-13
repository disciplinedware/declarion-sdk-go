# Declarion SDK for Go

Go SDK for building [Declarion](https://declarion.io) handler sidecars. Handles JSON-RPC 2.0 envelope parsing, continuation token verification, W3C trace propagation, platform API callbacks, and graceful shutdown. You write typed handler functions; the SDK handles the wire.

## Install

```bash
go get github.com/disciplinedware/declarion-sdk-go
```

## Quick start

```go
package main

import (
    "fmt"
    "log"

    sdk "github.com/disciplinedware/declarion-sdk-go/runtime"
)

type FetchParams struct {
    DryRun bool   `json:"dry_run"`
    ListID string `json:"list_id"`
}

type FetchResult struct {
    Companies  []map[string]any `json:"companies"`
    Leads      []map[string]any `json:"leads"`
    Activities []map[string]any `json:"activities"`
}

func handleFetch(ctx *sdk.HandlerCtx, p FetchParams) (FetchResult, error) {
    ctx.Logger.Info("fetching from external API", "list_id", p.ListID)

    // ... fetch and map data ...

    return FetchResult{
        Companies:  companies,
        Leads:      leads,
        Activities: activities,
    }, nil
}

type LoadParams struct {
    PreviousResult FetchResult `json:"previous_result"`
}

type LoadResult struct {
    CompaniesUpserted  int `json:"companies_upserted"`
    LeadsUpserted      int `json:"leads_upserted"`
    ActivitiesUpserted int `json:"activities_upserted"`
}

func handleLoad(ctx *sdk.HandlerCtx, p LoadParams) (LoadResult, error) {
    // Upsert via platform callbacks (auto-attaches auth + trace headers).
    companies, err := ctx.Platform.Data().BulkUpsert(ctx.Context, "company", "id", p.PreviousResult.Companies)
    if err != nil {
        return LoadResult{}, fmt.Errorf("upsert companies: %w", err)
    }

    // ... same for leads, activities ...

    return LoadResult{
        CompaniesUpserted: len(companies),
    }, nil
}

func main() {
    err := sdk.Serve(sdk.Config{
        Addr:        ":8080",
        PlatformURL: "http://declarion:3000",
    },
        sdk.Handler("myapp.fetch", handleFetch),
        sdk.Handler("myapp.load", handleLoad),
    )
    if err != nil {
        log.Fatal(err)
    }
}
```

## YAML wiring

Declare the handlers in your consumer app's schema:

```yaml
handlers:
  myapp.fetch:
    type: jsonrpc
    url: http://my-sidecar:8080/rpc
    timeout: 10m
    allow_no_objects: true
    params:
      dry_run: {type: bool, default: false}
      list_id: {type: string, required: false}
    result:
      companies: {type: json}
      leads: {type: json}
      activities: {type: json}

  myapp.load:
    type: jsonrpc
    url: http://my-sidecar:8080/rpc
    timeout: 5m
    allow_no_objects: true
    params:
      previous_result:
        companies: {type: json}
        leads: {type: json}
        activities: {type: json}
    result:
      companies_upserted: {type: int}

  myapp.import:
    async: true
    timeout: 15m
    allow_no_objects: true
    steps:
      - handler: myapp.fetch
      - handler: myapp.load

actions:
  myapp.import:
    type: handler
    handler: myapp.import
    scope: global
    display:
      name: {en: "Import Data"}
      icon: download
```

## Code generation

The hand-written YAML block above is the authority for product and dispatch
configuration: actions, display, permissions, async, timeout, idempotency,
invoke mode, audit, webhook security, and result schema. The SDK generator is
intentionally thin. It emits only the JSON-RPC handler stub - handler code,
`type: jsonrpc`, `${param.handlers_url}/rpc`, and params reflected from the Go
input struct.

The pattern is identical across every derivative project:

1. Copy `examples/gen-functions-yaml/main.go` from this SDK into your project at
   `cmd/gen-functions-yaml/main.go`. Edit only the blank-import block — point
   it at your project's aggregator package (the one whose `init()` chain calls
   `sdk.RegisterHandler` for every handler, action, and UDF).
2. Copy `examples/Makefile.snippet` verbatim into your Makefile. Same target
   names, same output path, same atomic-write pattern across every project.
3. Run `make gen-functions-yaml`. The generator emits
   `declarion/schema/_functions.generated.yaml`. The underscore prefix forces
   lex-first load order so your manual schema overlay wins via
   declarion-core's merge-on-duplicate policy.
4. Wire `make verify-functions-yaml` into CI next to `go test` as a drift
   guard.

The generator binary itself is a literal one-liner — `sdk.Generate()`. All
logic lives in the SDK; the per-project file contributes only the
blank-import manifest, which has to be there (Go can't import upward from SDK
into consumer code).

## What the SDK handles

- JSON-RPC 2.0 envelope parse/write
- Continuation token extraction and verification (JWT with `aud: handler_dispatch`)
- `Authorization: Bearer` forwarding on all platform callbacks
- `traceparent` header propagation
- `X-Declarion-Protocol-Version` assertion
- Typed `ctx.Platform.Data()` client for `/api/data/{entity}` (Get, List, Create, Update, Delete, BulkUpsert)
- Typed `ctx.Platform.Actions()` client for `/api/actions/{code}` (Invoke)
- Structured logging via `ctx.Logger` (`go.uber.org/zap`, pre-tagged with handler/tenant/user/audit_op)
- SIGTERM graceful shutdown
- `/health` endpoint for readiness probes
- Reserved JSON-RPC metadata extraction: `_entity_code` and `_object_ids` are removed from typed params and exposed on `ctx.EntityCode` / `ctx.ObjectIDs`
- Verifier serving for anonymous provider webhooks: a separate registry and token audience on the same `/rpc` endpoint (see below)

## Verifiers (anonymous provider webhooks)

A provider webhook (Stripe, Telegram, a payment gateway) reaches Declarion with no credential - the provider is not your user and will never hold your token. It can prove who it is (a signature over the body, a secret token it echoes back), but only your application knows how to check that proof.

A **verifier** is that check. You register it like a handler; Declarion calls it over the same `/rpc` endpoint, under a separate, powerless token audience, BEFORE it switches tenant, materializes a user, deduplicates the delivery, opens audit, or dispatches your handler.

```go
func init() {
	runtime.RegisterVerifier("acme.stripe", func(c *runtime.VerifierCtx) (runtime.VerifierResult, error) {
		// c.RawBody is the EXACT bytes (a signature is over bytes, not re-serialized JSON).
		// c.Header(...) / c.QueryValue(...) expose only what the declaration allowlisted.
		// c.PathValues holds the named segments of the endpoint's `path`.
		secret := lookupSecret(c.PathValues["provider"])
		if secret == "" || !validSignature(c.RawBody, c.Header("Stripe-Signature"), secret) {
			// Unknown provider and bad signature MUST be indistinguishable - this
			// response is public and must not become an oracle for what exists.
			return runtime.VerifierResult{}, runtime.Reject("not authenticated")
		}
		return runtime.VerifierResult{
			// Trusted values merged into the handler's params by name (they beat the
			// body: a contradicting body field is a 400 before dispatch).
			Params: map[string]any{"provider_id": id},
		}, nil
	})
}
```

**Returning identity.** `TargetTenantID` and `UserID` are both optional. Fill only what your code alone can know; Declarion fills the rest from the verifier declaration's `dispatch_tenant` / `dispatch_user`. A fixed-tenant provider (Stripe) returns neither - it never re-implements tenant or user lookup. A verifier whose tenant depends on the payload (which bot received this update) returns the tenant and leaves the user to the declaration.

**Declining.** Return one of three typed outcomes; Declarion maps each to a single uniform public response:

| | Public response | Use for |
|---|---|---|
| `runtime.Reject(reason)` | `401` | Any authentication or lookup failure. Keep them indistinguishable. |
| `runtime.InvalidRequest(reason)` | `400` | Authenticated, but the payload is malformed. |
| `runtime.Unavailable(format, ...)` | `503` | A platform problem, or a transient state the provider should retry through. |

A plain (non-typed) error is treated as `unavailable`, never as a rejection: your bug must not make the provider drop a delivery permanently. The `reason` is internal telemetry and never reaches the caller.

**`VerifierCtx.Platform`** is non-nil only when the declaration names a `run_as_global_user` - a service user Declarion mints a short-lived credential for, per call, so the verifier can read from the platform (typically to reveal an encrypted secret to compare against). It is NEVER built from the verifier's own call token, which carries no authority at all. That credential is minted *before* authentication succeeds, so keep its grants read/reveal-narrow.

`make gen-functions-yaml` emits the `verifiers:` stub (`type` + `url`) next to your handler stubs; the security-bearing fields (allowlists, dispatch identity, rate limit) are hand-written in your schema. See the platform's `dsl.md §15.16` for the full declaration.

## Integration tests

Use `testsdk.StartPlatform` to run consumer integration tests against a real
Declarion container plus Postgres. Prefer `testsdk.WithModuleDir` and point it
at the consumer's committed module root:

```go
env, err := testsdk.StartPlatform(
    testsdk.WithImage("ghcr.io/disciplinedware/declarion:0.24.0"),
    testsdk.WithModuleDir("../../modules/your-module"),
)
```

`WithModuleDir` requires the directory basename and `manifest.yaml:name` to
match. The SDK copies the module tree into the container through Docker's file
copy API; it does not bind-mount host paths. `WithSchema` and `WithMigrations`
remain for synthetic test modules.

`StartPlatform` bootstraps a REAL, DB-backed platform-operator test actor (not
merely a forged JWT claim): a `users` row, `_global` tenant ownership, and the
`platform_admin` role (mirroring declarion-core's own
`internal/auth/platform_operator.go` `grantPlatformOperatorTx` and migration
`097_superadmin_global_backfill.up.sql` exactly - same table shapes, same
constraint names). This is required because declarion-core's tenant-scoped
authority model (v0.29.0+,
`docs/plans/completed/2026-07-01-tenant-scoped-authority-model.md`) re-derives
authority LIVE from the database for any ASYNC job drain
(`internal/auth/service_principal.go` `LoadPrincipal` ->
`canAccessTenant`/`isSuperadminUser`) - it does not trust the JWT claims that
enqueued the job at all. A purely claims-forged actor with no real DB row
would dead-letter with `user is not a member of tenant` the first time a test
triggers an async job (creating a second tenant is the common case -
`tenant.__create` schedules an auto-seed job). `env.NewCtx`'s default context
(standing in the bootstrapped "test" tenant) is this same real actor, so it
passes both synchronous AND async-drain authority checks out of the box.

Writing an `access: superadmin` platform entity directly (`user`, `tenant` -
see declarion-core's `checkEntityAccess` / `EntityAccessSuperadmin`) is a
narrower, separate requirement: the caller's ACTIVE TenantID claim must
literally be `_global` for that one synchronous request (a claims-only check,
no DB lookup - unaffected by having a real DB-backed actor). Use
`testsdk.WithGlobalTenant()` for that one call, e.g. when a test needs to
create a SECOND tenant:

```go
ctx := env.NewCtx(t, testsdk.WithGlobalTenant())
_, err := ctx.Platform.Data().BulkCreate(ctx.Context, "tenant", map[string]any{...})
```

Do not invent an ad hoc claim shape for this - `WithGlobalTenant()` mirrors
declarion-core's own coverage for the same gate
(`internal/store/dbtest/cross_tenant_write_location_test.go`:
`TenantID: engine.ZeroTenantID, IsSuperadmin: true`). Everyday test contexts
(regular entity/action calls) keep using the default `env.NewCtx(t)`.

Tests that need to assert post-migration database invariants can use
`env.DBPool(t)` to query the same Postgres database that `StartPlatform`
already migrated. This is for read-mostly test assertions such as
`information_schema` checks; production code and handler tests should still
prefer platform APIs. In external mode (`DECLARION_TEST_URL`), set
`DECLARION_TEST_DATABASE_URL` so `DBPool` knows which database backs the
external platform.

## Error handling

Return `*runtime.AppError` for structured JSON-RPC errors:

```go
return result, &sdk.AppError{
    Code:          sdk.JSONRPCAppError,
    Message:       "ClickUp API rate limit",
    DeclarionCode: sdk.CodeRateLimited,
    Retryable:     true,
}
```

Any other `error` maps to `INTERNAL_ERROR`.

## Conformance test suite

The SDK ships a conformance harness that validates any sidecar (Go, Python, TS, or raw) against the Declarion wire contract. Run it against a running sidecar:

```bash
go run ./conformance/cmd/conformance-harness http://localhost:8080
```

Or run in-process against the Go SDK:

```bash
go test ./conformance/ -v
```

## Environment variables

| Variable | Description | Default |
|---|---|---|
| `DECLARION_PLATFORM_URL` | Platform base URL for callbacks | (required) |
| `DECLARION_JWT_SECRET` | JWT secret for token verification | (empty = no verification) |
| `DECLARION_SIDECAR_ADDR` | Listen address | `:8080` |

## License

MIT
