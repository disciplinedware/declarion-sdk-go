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

func handleFetch(ctx *sdk.Ctx, p FetchParams) (FetchResult, error) {
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

func handleLoad(ctx *sdk.Ctx, p LoadParams) (LoadResult, error) {
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

## What the SDK handles

- JSON-RPC 2.0 envelope parse/write
- Continuation token extraction and verification (JWT with `aud: handler_dispatch`)
- `Authorization: Bearer` forwarding on all platform callbacks
- `traceparent` header propagation
- `X-Declarion-Protocol-Version` assertion
- Typed `ctx.Platform.Data()` client for `/api/data/{entity}` (Get, List, Create, Update, Delete, BulkUpsert)
- Typed `ctx.Platform.Actions()` client for `/api/actions/{code}` (Invoke)
- Structured logging via `ctx.Logger` (slog, pre-tagged with handler/tenant/user/audit_op)
- SIGTERM graceful shutdown
- `/health` endpoint for readiness probes

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
