package platform

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

// This client declares its OWN types in Go rather than in a module schema, so
// the loader's rules never run over them. They are the same rules, and this is
// where they run.
//
// The one that bit: a member named `status` is dropped at serialization,
// because RFC 9457 already gives the object a member of that name - so a peer's
// 429 came back as this type's own 502, which is the exact class of lost status
// the one-object contract exists to end.
func TestTheClientsOwnTypesAreDeclaredLegally(t *testing.T) {
	require.NotEmpty(t, transportTypes)
	for code, def := range transportTypes {
		assert.True(t, errs.ValidCode(code), "%s is not spelled <owner>.<name>", code)
		require.NotNil(t, def, "%s is declared with no body", code)
		assert.NotEmpty(t, def.TitleFor("en"), "%s has no title", code)
		for member := range def.Fields {
			assert.True(t, errs.ValidMemberName(member),
				"%s: member %q is not a legal RFC 9457 member name", code, member)
			assert.False(t, errs.IsKnownMember(member),
				"%s: member %q is one RFC 9457 already gives the object, and serialization drops it", code, member)
		}
	}
}

// StatusOf must answer the same before and after the object is serialized. It
// did not: the peer's status travelled in a member the marshaller dropped.
func TestThePeerStatusSurvivesASerializationRoundTrip(t *testing.T) {
	raised := errorFromResponse(http.StatusTooManyRequests, []byte("slow down"), "/api/actions/x", "text/plain")

	before, ok := StatusOf(raised)
	require.True(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, before)

	b, err := json.Marshal(raised)
	require.NoError(t, err)
	var decoded errs.Error
	require.NoError(t, json.Unmarshal(b, &decoded))

	after, ok := StatusOf(&decoded)
	require.True(t, ok)
	assert.Equal(t, before, after, "the peer's status must not become this type's own 502")
}
