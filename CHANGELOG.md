# Changelog

All notable changes to `declarion-sdk-go` will be documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
