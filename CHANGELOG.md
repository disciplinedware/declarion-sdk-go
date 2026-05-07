# Changelog

All notable changes to `declarion-sdk-go` will be documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[Unreleased]: https://github.com/disciplinedware/declarion-sdk-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/disciplinedware/declarion-sdk-go/releases/tag/v0.1.0
