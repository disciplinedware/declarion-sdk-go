package runtime

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func setupYAMLGenTest(t *testing.T) {
	t.Helper()
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
}

func registerEchoFn(code string) {
	RegisterHandler[echoParams, echoResult](code, func(_ *HandlerCtx, _ echoParams) (echoResult, error) {
		return echoResult{}, nil
	})
}

func TestGenerateFunctionsYAMLEmptyRegistry(t *testing.T) {
	setupYAMLGenTest(t)
	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))
	assert.Equal(t, "handlers:\n", buf.String())
}

func TestGenerateFunctionsYAMLThinHandlerWithParams(t *testing.T) {
	setupYAMLGenTest(t)
	registerEchoFn("sw.actions.echo")

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))
	out := buf.String()
	assert.NotContains(t, out, "actions:")
	assert.NotContains(t, out, "display:")
	assert.NotContains(t, out, "timeout:")

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))
	entry := parsed.Handlers["sw.actions.echo"]
	require.NotNil(t, entry)
	assert.Equal(t, "jsonrpc", entry["type"])
	assert.Equal(t, "${param.handlers_url}/rpc", entry["url"])
	params, ok := entry["params"].(map[string]any)
	require.True(t, ok)
	name, ok := params["name"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", name["type"])
}

func TestGenerateFunctionsYAMLParamOrderAndFields(t *testing.T) {
	setupYAMLGenTest(t)
	type params struct {
		First  string `json:"first" required:"true"`
		Second bool   `json:"second" default:"true" hidden:"true"`
		Third  string `json:"third" enum:"stage" sensitive:"true"`
	}
	RegisterHandler[params, echoResult]("ordered", func(*HandlerCtx, params) (echoResult, error) { return echoResult{}, nil })

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))
	out := buf.String()
	assert.Less(t, strings.Index(out, "first:"), strings.Index(out, "second:"))
	assert.Less(t, strings.Index(out, "second:"), strings.Index(out, "third:"))
	assert.Contains(t, out, "first: {type: string, required: true}")
	assert.Contains(t, out, "second: {type: bool, default: \"true\", hidden: true}")
	assert.Contains(t, out, "third: {type: string, sensitive: true, enum: stage}")
}

func TestGenerateFunctionsYAMLDeterministicAlphabeticalOrder(t *testing.T) {
	setupYAMLGenTest(t)
	for _, code := range []string{"z.handler", "m.handler", "a.handler"} {
		registerEchoFn(code)
	}
	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))
	out := buf.String()
	assert.Less(t, strings.Index(out, "a.handler"), strings.Index(out, "m.handler"))
	assert.Less(t, strings.Index(out, "m.handler"), strings.Index(out, "z.handler"))
}

func TestRegisterHandlerPanicsOnDuplicate(t *testing.T) {
	setupYAMLGenTest(t)
	registerEchoFn("dup.method")
	assert.Panics(t, func() { registerEchoFn("dup.method") })
}
