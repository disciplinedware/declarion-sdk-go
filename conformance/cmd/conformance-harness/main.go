// conformance-harness is the CLI for running the Declarion JSON-RPC conformance
// test suite against a sidecar. Usage: conformance-harness <sidecar-url>
package main

import (
	"fmt"
	"os"

	"github.com/disciplinedware/declarion-sdk-go/conformance"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: conformance-harness <sidecar-url>\n")
		os.Exit(1)
	}

	sidecarURL := os.Args[1]
	h := conformance.NewHarness(sidecarURL)
	results := h.RunAll()

	passed, failed := 0, 0
	for _, r := range results {
		if r.Passed {
			fmt.Printf("  PASS  %s\n", r.Name)
			passed++
		} else {
			fmt.Printf("  FAIL  %s: %s\n", r.Name, r.Error)
			failed++
		}
	}

	fmt.Printf("\n%d passed, %d failed, %d total\n", passed, failed, passed+failed)
	if failed > 0 {
		os.Exit(1)
	}
}
