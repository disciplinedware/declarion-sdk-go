// Canonical template for a derivative project's functions YAML generator.
//
// Copy this file to your project at cmd/gen-functions-yaml/main.go.
// The only edit needed is the blank-import block below — replace the
// placeholder with one or more packages whose init() functions call
// sdk.RegisterFunction (typically a single aggregator package that itself
// blank-imports every handler/action/udf subpackage).
//
// Wire the Makefile target via the snippet in
// declarion-sdk-go/examples/Makefile.snippet — same target names,
// byte-for-byte across every derivative project. Output path stays at
// declarion/schema/_functions.generated.yaml for engine/core projects;
// application-module projects (e.g. declarion-crm) use
// modules/<module-name>/schema/_functions.generated.yaml instead. The
// Makefile snippet documents this single supported deviation.
//
// Build identical across all derivative projects:
//
//	make gen-functions-yaml      # regenerate declarion/schema/_functions.generated.yaml
//	make verify-functions-yaml   # CI drift guard — fails if regen would change disk copy
package main

import (
	sdk "github.com/disciplinedware/declarion-sdk-go/runtime"

	// REPLACE: blank-import the aggregator package(s) whose init() functions
	// register every handler/action/udf in this project. Convention: maintain
	// a single aggregator (e.g. internal/handlers/all) that itself imports
	// each subpackage; then list only the aggregator here.
	//
	// _ "github.com/your-org/your-project/internal/handlers/all"
)

func main() { sdk.RunGenerator() }
