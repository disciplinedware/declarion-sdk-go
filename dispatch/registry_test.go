package dispatch

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testCtx struct{ Prefix string }
type otherCtx struct{ Suffix string }

type echoParams struct {
	Name string `json:"name" required:"true"`
}

type echoResult struct {
	Message string `json:"message"`
}

func TestRegistryExecuteRoundTripTypedContext(t *testing.T) {
	reg := NewRegistry[testCtx]()
	RegisterHandler[testCtx, echoParams, echoResult](reg, "echo", func(ctx testCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: ctx.Prefix + p.Name}, nil
	})

	got, err := reg.Execute("echo", testCtx{Prefix: "hi "}, json.RawMessage(`{"name":"Ada"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"hi Ada"}`, string(got))

	decls := reg.Declarations()
	require.Len(t, decls, 1)
	assert.Equal(t, "echo", decls[0].Code)
	require.Len(t, decls[0].Params, 1)
	assert.Equal(t, "name", decls[0].Params[0].Name)
	assert.True(t, decls[0].Params[0].Required)
}

func TestRegistryExecuteSupportsDistinctContextTypes(t *testing.T) {
	reg := NewRegistry[otherCtx]()
	RegisterHandler[otherCtx, echoParams, echoResult](reg, "echo", func(ctx otherCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: p.Name + ctx.Suffix}, nil
	})
	got, err := reg.Execute("echo", otherCtx{Suffix: "!"}, json.RawMessage(`{"name":"Ada"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"Ada!"}`, string(got))
}

func TestRegistryExecuteTypedNilResultIsJSONNull(t *testing.T) {
	type result struct {
		OK bool `json:"ok"`
	}
	reg := NewRegistry[testCtx]()
	RegisterHandler[testCtx, echoParams, *result](reg, "nil", func(testCtx, echoParams) (*result, error) {
		return nil, nil
	})
	got, err := reg.Execute("nil", testCtx{}, json.RawMessage(`{"name":"Ada"}`))
	require.NoError(t, err)
	assert.Equal(t, "null", string(got))
}

func TestRegistryDuplicatePanics(t *testing.T) {
	reg := NewRegistry[testCtx]()
	RegisterHandler[testCtx, echoParams, echoResult](reg, "dup", func(testCtx, echoParams) (echoResult, error) { return echoResult{}, nil })
	assert.Panics(t, func() {
		RegisterHandler[testCtx, echoParams, echoResult](reg, "dup", func(testCtx, echoParams) (echoResult, error) { return echoResult{}, nil })
	})
}

func TestRegistryDecodeError(t *testing.T) {
	reg := NewRegistry[testCtx]()
	RegisterHandler[testCtx, echoParams, echoResult](reg, "echo", func(testCtx, echoParams) (echoResult, error) { return echoResult{}, nil })
	_, err := reg.Execute("echo", testCtx{}, json.RawMessage(`{"name": 1}`))
	e, ok := errs.From(err)
	require.True(t, ok, "err = %v", err)
	require.Equal(t, "action.invalid_params", e.Code())
}
