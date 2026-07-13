package runtime

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	kern "github.com/disciplinedware/declarion-sdk-go/dispatch"
	"github.com/disciplinedware/declarion-sdk-go/handlerparam"
)

// GenerateFunctionsYAML walks the process-wide handler registry and emits a
// thin YAML stub compatible with declarion-core's loader. The generated stub
// contains only the JSON-RPC wire declaration and params derived from Go.
func GenerateFunctionsYAML(out io.Writer) error {
	decls := registeredDeclarations()
	sort.Slice(decls, func(i, j int) bool { return decls[i].Code < decls[j].Code })

	if _, err := fmt.Fprintln(out, "handlers:"); err != nil {
		return fmt.Errorf("write handlers header: %w", err)
	}
	for _, d := range decls {
		if err := writeHandlerYAML(out, d); err != nil {
			return fmt.Errorf("write handler %s: %w", d.Code, err)
		}
	}

	// Verifiers registry: a sibling of handlers, emitted the same way - from
	// declarations, as type/url stubs. Hand-written overlay YAML adds
	// run_as_global_user / allowed_headers / allowed_query / config /
	// timeout_seconds / rate_limit; the generated stub carries only the wire
	// declaration (verifiers have no typed params - their input is the request
	// envelope on VerifierCtx).
	vdecls := registeredVerifierDeclarations()
	if len(vdecls) > 0 {
		if _, err := fmt.Fprintln(out, "verifiers:"); err != nil {
			return fmt.Errorf("write verifiers header: %w", err)
		}
		for _, d := range vdecls {
			if err := writeVerifierYAML(out, d); err != nil {
				return fmt.Errorf("write verifier %s: %w", d.Code, err)
			}
		}
	}
	return nil
}

func writeVerifierYAML(out io.Writer, d kern.Declaration) error {
	lines := []string{
		fmt.Sprintf("  %s:", d.Code),
		"    type: jsonrpc",
		"    url: ${param.handlers_url}/rpc",
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func writeHandlerYAML(out io.Writer, d kern.Declaration) error {
	lines := []string{
		fmt.Sprintf("  %s:", d.Code),
		"    type: jsonrpc",
		"    url: ${param.handlers_url}/rpc",
	}
	if len(d.Params) > 0 {
		lines = append(lines, "    params:")
		for _, p := range d.Params {
			lines = append(lines, formatParamLine(p))
		}
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func formatParamLine(p handlerparam.Param) string {
	fields := []string{}
	if p.Type != "" {
		fields = append(fields, "type: "+p.Type)
	}
	if p.Required {
		fields = append(fields, "required: true")
	}
	if p.Default != nil {
		fields = append(fields, "default: "+formatScalar(p.Default))
	}
	if p.Hidden {
		fields = append(fields, "hidden: true")
	}
	if p.Sensitive {
		fields = append(fields, "sensitive: true")
	}
	if p.Enum != "" {
		fields = append(fields, "enum: "+p.Enum)
	}
	return fmt.Sprintf("      %s: {%s}", p.Name, strings.Join(fields, ", "))
}

func formatScalar(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case fmt.Stringer:
		return strconv.Quote(x.String())
	default:
		return fmt.Sprintf("%v", x)
	}
}
