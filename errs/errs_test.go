package errs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

func testCatalogue() errs.Catalogue {
	return errs.Catalogue{
		"entity.stale_object": {
			Status:    409,
			Retryable: false,
			Title: errs.LocalizedString{
				"en": "The record changed since you loaded it.",
				"ru": "Запись изменилась с момента загрузки.",
			},
			Fields: map[string]string{"row_version": "integer", "stored_row_version": "integer"},
		},
		"transport.stream_interrupted": {
			Status:    0,
			Retryable: true,
			Title:     errs.LocalizedString{"en": "The connection broke before the answer finished."},
		},
		errs.CodeUndeclaredType: {
			Status:    500,
			Retryable: false,
			Title: errs.LocalizedString{
				"en": "This action failed in a way this deployment cannot describe.",
				"ru": "Действие завершилось ошибкой, которую это развёртывание не может описать.",
			},
		},
		errs.CodeTooLarge: {
			Status:    500,
			Retryable: false,
			Title:     errs.LocalizedString{"en": "The failure report was too large to return."},
		},
		"zz.only_ru": {
			Status: 400,
			Title:  errs.LocalizedString{"ru": "Только по-русски."},
		},
	}
}

func TestCodeAndType(t *testing.T) {
	tests := []struct {
		name     string
		typeURI  string
		wantCode string
	}{
		{name: "declared_type", typeURI: "/errors/entity.stale_object", wantCode: "entity.stale_object"},
		{name: "about_blank_has_no_code", typeURI: errs.TypeUnknown, wantCode: ""},
		{name: "empty_type_has_no_code", typeURI: "", wantCode: ""},
		{name: "absolute_uri_keeps_last_segment", typeURI: "https://example.com/errors/auth.expired", wantCode: "auth.expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &errs.Error{Type: tt.typeURI}
			assert.Equal(t, tt.wantCode, e.Code())
		})
	}
}

func TestNewFillsStatusAndRetryabilityFromTheCatalogue(t *testing.T) {
	errs.SetCatalogue(testCatalogue(), "en")
	t.Cleanup(func() { errs.SetCatalogue(nil, "") })

	e := errs.New("transport.stream_interrupted")
	assert.Equal(t, "/errors/transport.stream_interrupted", e.Type)
	assert.True(t, e.Retryable, "retryability is the type's, read at raise time")
	assert.Empty(t, e.Title, "a raise site has no caller locale, so it never fills a title")
}

func TestNewWithoutCatalogueStillWorks(t *testing.T) {
	errs.SetCatalogue(nil, "")

	e := errs.New("swiftward-community.decision_locked", errs.Args{"channel_id": 7})
	assert.Equal(t, "/errors/swiftward-community.decision_locked", e.Type)
	assert.Equal(t, 0, e.Status)
	v, ok := e.Ext("channel_id")
	assert.True(t, ok)
	assert.Equal(t, 7, v)
}

func TestNewRejectsASecondArgs(t *testing.T) {
	assert.Panics(t, func() {
		errs.New("entity.stale_object", errs.Args{"a": 1}, errs.Args{"b": 2})
	})
}

func TestMarshalPutsDeclaredFieldsAtTheTopLevel(t *testing.T) {
	e := &errs.Error{
		Type:      "/errors/entity.stale_object",
		Status:    409,
		Title:     "The record changed since you loaded it.",
		Detail:    "row_version 7 does not match stored 9",
		Instance:  "/admin/audit/operations/7f3a",
		Retryable: false,
		Fields:    map[string]any{"row_version": 7, "stored_row_version": 9},
	}

	b, err := json.Marshal(e)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, float64(7), m["row_version"], "a declared field is a top-level member, not a nested bag")
	assert.Equal(t, float64(9), m["stored_row_version"])
	assert.Equal(t, "/errors/entity.stale_object", m["type"])
	assert.Equal(t, false, m["retryable"], "retryable is always emitted, never absent")
}

func TestMarshalOmitsTheBoundaryMembersAStoredOccurrenceNeverHas(t *testing.T) {
	errs.SetCatalogue(testCatalogue(), "en")
	t.Cleanup(func() { errs.SetCatalogue(nil, "") })

	raised := errs.New("transport.stream_interrupted")
	b, err := json.Marshal(raised)
	require.NoError(t, err)
	stored := map[string]any{}
	require.NoError(t, json.Unmarshal(b, &stored))
	assert.NotContains(t, stored, "title")
	assert.NotContains(t, stored, "status")
	assert.NotContains(t, stored, "instance")

	b, err = json.Marshal(errs.Render(raised, errs.RenderContext{
		Catalogue: testCatalogue(), Locale: "en", Instance: "/requests/r1",
	}))
	require.NoError(t, err)
	rendered := map[string]any{}
	require.NoError(t, json.Unmarshal(b, &rendered))
	assert.Equal(t, "The connection broke before the answer finished.", rendered["title"])
	assert.Equal(t, "/requests/r1", rendered["instance"])
}

func TestMarshalNeverSerializesTheCause(t *testing.T) {
	const sentinel = `relation "declarion.users" does not exist`
	e := errs.New("entity.stale_object").Because(errors.New(sentinel))

	b, err := json.Marshal(e)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "declarion.users")
	assert.Contains(t, e.Error(), sentinel, "the operator string carries what the wire never does")
}

func TestMarshalDoesNotLetAFieldOverwriteAKnownMember(t *testing.T) {
	e := &errs.Error{
		Type:   "/errors/entity.stale_object",
		Title:  "real title",
		Fields: map[string]any{"title": "forged", "status": 418},
	}
	b, err := json.Marshal(e)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "real title", m["title"])
	assert.NotContains(t, m, "status", "an unset known member stays absent")
}

func TestUnmarshalCollectsUnknownMembersAsFields(t *testing.T) {
	const body = `{"type":"/errors/llm-connector.upstream_error","status":502,` +
		`"title":"The provider refused the request.","detail":"provider returned 401",` +
		`"retryable":false,"upstream_status":401,"provider":"openai"}`

	var e errs.Error
	require.NoError(t, json.Unmarshal([]byte(body), &e))

	assert.Equal(t, "llm-connector.upstream_error", e.Code())
	assert.Equal(t, 502, e.Status)
	assert.False(t, e.Retryable)
	got, ok := e.ExtInt("upstream_status")
	require.True(t, ok, "a JSON number reaches a consumer as an int")
	assert.Equal(t, 401, got)
	provider, ok := e.ExtString("provider")
	require.True(t, ok)
	assert.Equal(t, "openai", provider)
}

func TestRoundTripSurvivesEveryMember(t *testing.T) {
	in := &errs.Error{
		Type:      "/errors/entity.stale_object",
		Status:    409,
		Title:     "changed",
		Detail:    "diag",
		Instance:  "/admin/audit/operations/1",
		Retryable: true,
		Fields:    map[string]any{"row_version": 7},
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)

	var out errs.Error
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, in.Type, out.Type)
	assert.Equal(t, in.Status, out.Status)
	assert.Equal(t, in.Title, out.Title)
	assert.Equal(t, in.Detail, out.Detail)
	assert.Equal(t, in.Instance, out.Instance)
	assert.Equal(t, in.Retryable, out.Retryable)
	rv, ok := out.ExtInt("row_version")
	require.True(t, ok)
	assert.Equal(t, 7, rv)
}

func TestFrom(t *testing.T) {
	errs.SetCatalogue(nil, "")
	typed := errs.New("entity.stale_object")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "direct", err: typed, want: true},
		{name: "wrapped", err: fmt.Errorf("outer: %w", typed), want: true},
		{name: "plain_error", err: errors.New("boom"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := errs.From(tt.err)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				require.NotNil(t, got)
				assert.NotEmpty(t, got.Type)
			}
		})
	}
}

func TestIsAnswersRetryabilityFromTheDeclaredValue(t *testing.T) {
	errs.SetCatalogue(testCatalogue(), "en")
	t.Cleanup(func() { errs.SetCatalogue(nil, "") })

	assert.True(t, errors.Is(errs.New("transport.stream_interrupted"), errs.ErrRetryable))
	assert.False(t, errors.Is(errs.New("entity.stale_object"), errs.ErrRetryable))
	assert.True(t, errors.Is(fmt.Errorf("wrapped: %w", errs.New("transport.stream_interrupted")), errs.ErrRetryable))
	assert.False(t, errors.Is(errs.New("transport.stream_interrupted"), context.Canceled),
		"the sentinel answer is scoped to ErrRetryable")
}

func TestUnwrapReachesTheCause(t *testing.T) {
	e := errs.New("entity.stale_object").Because(context.Canceled)
	assert.True(t, errors.Is(e, context.Canceled), "a caller branching on the cause keeps working")
}

func TestRender(t *testing.T) {
	cat := testCatalogue()
	rc := errs.RenderContext{Catalogue: cat, Locale: "ru", DefaultLocale: "en", Instance: "/admin/audit/operations/9"}

	t.Run("declared_type_takes_status_retryability_and_title_from_the_declaration", func(t *testing.T) {
		out := errs.Render(&errs.Error{
			Type: "/errors/entity.stale_object", Status: 418, Title: "forged", Instance: "/forged",
			Detail: "row_version 7 does not match stored 9",
		}, rc)
		assert.Equal(t, 409, out.Status)
		assert.Equal(t, "Запись изменилась с момента загрузки.", out.Title)
		assert.Equal(t, "/admin/audit/operations/9", out.Instance)
		assert.Equal(t, "row_version 7 does not match stored 9", out.Detail, "detail is the producer's and survives")
	})

	t.Run("undeclared_type_keeps_its_identity_and_says_nothing_else", func(t *testing.T) {
		out := errs.Render(&errs.Error{
			Type: "/errors/somebody.invented_this", Detail: "unvetted text from a sidecar",
		}, rc)
		assert.Equal(t, "/errors/somebody.invented_this", out.Type, "identity survives")
		assert.Equal(t, "Действие завершилось ошибкой, которую это развёртывание не может описать.", out.Title)
		assert.Empty(t, out.Detail, "nobody vetted that text")
		assert.Equal(t, 500, out.Status)
		assert.False(t, out.Retryable)
	})

	t.Run("title_falls_back_per_type_not_per_deployment", func(t *testing.T) {
		out := errs.Render(&errs.Error{Type: "/errors/zz.only_ru"}, errs.RenderContext{
			Catalogue: cat, Locale: "en", DefaultLocale: "en",
		})
		assert.Equal(t, "Только по-русски.", out.Title,
			"a type declaring no en title answers its own declared one, never an empty string")
	})

	t.Run("empty_type_renders_about_blank", func(t *testing.T) {
		out := errs.Render(&errs.Error{}, rc)
		assert.Equal(t, errs.TypeUnknown, out.Type)
	})

	t.Run("over_bound_is_replaced_not_truncated", func(t *testing.T) {
		out := errs.Render(&errs.Error{
			Type:   "/errors/entity.stale_object",
			Fields: map[string]any{"blob": strings.Repeat("x", 40000)},
		}, errs.RenderContext{Catalogue: cat, Locale: "en", DefaultLocale: "en", MaxBytes: 1024})
		assert.Equal(t, "platform.error_too_large", out.Code())
		offending, ok := out.ExtString("offending_type")
		require.True(t, ok)
		assert.Equal(t, "/errors/entity.stale_object", offending)
		assert.NotContains(t, out.Fields, "blob", "the original survives only as the logged cause")

		b, err := json.Marshal(out)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(b), 1024)
	})

	t.Run("render_does_not_mutate_the_producers_fields", func(t *testing.T) {
		in := &errs.Error{Type: "/errors/entity.stale_object", Fields: map[string]any{"row_version": 7}}
		out := errs.Render(in, rc)
		out.Fields["injected"] = true
		assert.NotContains(t, in.Fields, "injected")
	})
}

func TestBoundedReplacesOnlyOverTheBound(t *testing.T) {
	small := errs.New("entity.stale_object")
	assert.Same(t, small, errs.Bounded(small, 1024))

	big := &errs.Error{Type: "/errors/entity.stale_object", Fields: map[string]any{"blob": strings.Repeat("x", 4000)}}
	got := errs.Bounded(big, 1024)
	assert.Equal(t, "platform.error_too_large", got.Code())
	assert.Nil(t, errs.Bounded(nil, 1024))
}

func TestValidCode(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "core_domain_word", code: "auth.invalid_credentials", want: true},
		{name: "hyphenated_manifest_name", code: "swiftward-llm-gateway.model_unavailable", want: true},
		{name: "upper_snake_is_the_old_spelling", code: "STALE_OBJECT", want: false},
		{name: "no_owner", code: "stale_object", want: false},
		{name: "underscore_in_owner", code: "swiftward_community.decision_locked", want: false},
		{name: "trailing_dot", code: "auth.", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errs.ValidCode(tt.code))
		})
	}
}

func TestValidMemberName(t *testing.T) {
	tests := []struct {
		name   string
		member string
		want   bool
	}{
		{name: "declared_member", member: "upstream_status", want: true},
		{name: "three_characters", member: "row", want: true},
		{name: "too_short", member: "id", want: false},
		{name: "starts_with_a_digit", member: "1st_try", want: false},
		{name: "hyphen_is_not_alpha_digit_underscore", member: "upstream-status", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errs.ValidMemberName(tt.member))
		})
	}
}

// A nil *Error inside a non-nil interface is a shape errors.Is and errors.As
// walk into while looking for something else. The chain-walking methods must
// not take the process down on a path that has nothing to do with this error.
func TestANilErrorTravellingAsAnErrorDoesNotPanic(t *testing.T) {
	var absent *errs.Error
	var asErr error = absent

	assert.NotPanics(t, func() {
		_ = errors.Unwrap(asErr)
		_ = errors.Is(asErr, errs.ErrRetryable)
		var target *errs.Error
		_ = errors.As(asErr, &target)
		_ = absent.Error()
		_ = absent.Code()
	})
}

// RFC 9457 §3.1: a consumer ignores a member whose value is not the form it
// expects. Rejecting the object over one advisory number threw away the TYPE,
// which is the only member a consumer branches on.
func TestAMalformedStatusIsIgnoredNotFatal(t *testing.T) {
	var e errs.Error
	require.NoError(t, json.Unmarshal(
		[]byte(`{"type":"/errors/entity.stale_object","status":409.5,"row_version":7}`), &e))
	assert.Equal(t, "entity.stale_object", e.Code(), "the type survives a malformed status")
	assert.Zero(t, e.Status, "a status that is not a whole number is not read")
	v, ok := e.ExtInt("row_version")
	assert.True(t, ok)
	assert.Equal(t, 7, v, "the declared members survive too")
}
