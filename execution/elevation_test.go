package execution

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeStrictSelection(t *testing.T) {
	for _, input := range []string{"null", "[]", `{"system":null}`, `{"system":true,"system":false}`, `{"extra":true}`, `{"grants":[null]}`, `{"grants":[1]}`, `{"grants":null}`, `{"system":"true"}`} {
		t.Run(input, func(t *testing.T) {
			_, err := Decode(base64.RawURLEncoding.EncodeToString([]byte(input)))
			require.Error(t, err)
		})
	}
	encoded, err := Encode(Elevation{System: true, Grants: []string{"b", "a", "a"}})
	require.NoError(t, err)
	e, err := Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, Elevation{System: true, Grants: []string{"a", "b"}}, e)
	require.False(t, (Elevation{Grants: []string{"a"}}).Contains(e))
	for _, invalid := range []string{encoded + "=", encoded + "\n", encoded[:2] + "\r\n" + encoded[2:], "!"} {
		_, err := Decode(invalid)
		require.Error(t, err)
	}
}
