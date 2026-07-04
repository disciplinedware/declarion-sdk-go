package handlerparam

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReflectParamsMinimalFields(t *testing.T) {
	type params struct {
		Name    string         `json:"name" required:"true" default:"unknown"`
		Age     int            `json:"age" hidden:"true"`
		Cost    float64        `json:"cost"`
		Active  bool           `json:"active"`
		At      time.Time      `json:"at"`
		Tags    []string       `json:"tags"`
		Meta    map[string]any `json:"meta"`
		Stage   string         `json:"stage" enum:"lead_stage"`
		Secret  string         `json:"secret" type:"password" sensitive:"true"`
		Ignored string         `json:"-"`
	}

	got := ReflectParams[params]()
	require.Len(t, got, 9)
	assert.Equal(t, []string{"name", "age", "cost", "active", "at", "tags", "meta", "stage", "secret"}, names(got))
	assert.Equal(t, "string", got[0].Type)
	assert.True(t, got[0].Required)
	assert.Equal(t, "unknown", got[0].Default)
	assert.Equal(t, "int", got[1].Type)
	assert.True(t, got[1].Hidden)
	assert.Equal(t, "float", got[2].Type)
	assert.Equal(t, "bool", got[3].Type)
	assert.Equal(t, "timestamp", got[4].Type)
	assert.Equal(t, "json", got[5].Type)
	assert.Equal(t, "json", got[6].Type)
	assert.Equal(t, "lead_stage", got[7].Enum)
	assert.Equal(t, "password", got[8].Type)
	assert.True(t, got[8].Sensitive)
}

func TestReflectParamsRejectsDuplicateJSONTags(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(""), Tag: `json:"same"`},
		{Name: "B", Type: reflect.TypeOf(""), Tag: `json:"same"`},
	})
	assert.Panics(t, func() { reflectParams(typ) })
}

func TestReflectParamsRejectsUnsupportedKind(t *testing.T) {
	type params struct {
		Bad chan int `json:"bad"`
	}
	assert.Panics(t, func() { ReflectParams[params]() })
}

func TestReflectParamsHasNoDisplayOrRefSurface(t *testing.T) {
	type params struct {
		Account string `json:"account" ref:"account" name_en:"Account"`
	}
	got := ReflectParams[params]()
	require.Len(t, got, 1)
	assert.Equal(t, "account", got[0].Name)
	assert.Equal(t, "string", got[0].Type)
}

func names(params Params) []string {
	out := make([]string, len(params))
	for i, p := range params {
		out[i] = p.Name
	}
	return out
}
