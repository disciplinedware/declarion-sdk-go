package errs

import (
	"fmt"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// LocalizedString is a sentence per language code - the shape every display
// name in a Declarion module already declares. A bare scalar is read as
// English, exactly as it is everywhere else in the DSL.
type LocalizedString map[string]string

func (ls *LocalizedString) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*ls = LocalizedString{"en": value.Value}
		return nil
	case yaml.MappingNode:
		var m map[string]string
		if err := value.Decode(&m); err != nil {
			return fmt.Errorf("decode localized string map: %w", err)
		}
		*ls = LocalizedString(m)
		return nil
	default:
		return fmt.Errorf("localized string: expected string or map, got %v", value.Kind)
	}
}

// TypeDef is one declared class of failure, as RFC 9457 Section 4 defines a
// problem type: an identifier, a title, and the HTTP status it is used with.
// Retryability and the permitted member names are ours.
type TypeDef struct {
	// Status is the HTTP status the platform returns when this type surfaces
	// over HTTP. It is the only place a status is decided, so a handler
	// influences its own by declaring it.
	Status int `yaml:"status" json:"status"`
	// Retryable answers whether the same call can succeed later. It does NOT
	// answer whether repeating is safe - that is idempotency.
	Retryable bool `yaml:"retryable" json:"retryable"`
	// Title is the complete, stable sentence a person reads. No value is ever
	// substituted into it.
	Title LocalizedString `yaml:"title" json:"title"`
	// Fields names the members this type may carry and their declared types.
	// The call-site gate reads it; nothing enforces it at runtime, because
	// failing loudly while reporting a failure is the wrong direction.
	Fields map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`
	// Deprecated marks a type that is no longer raised. Its declaration stays
	// so a consumer branching on it keeps parsing; it simply stops matching.
	Deprecated bool `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
}

// Catalogue is the merge of every loaded module's error types, keyed by the
// code exactly as declared. One per process; two processes hold the same one
// only if they were given the same ordered module set.
type Catalogue map[string]*TypeDef

// Lookup answers the declaration for a code.
func (c Catalogue) Lookup(code string) (*TypeDef, bool) {
	def, ok := c[code]
	return def, ok && def != nil
}

// TitleFor returns the first non-blank title among the candidate locales,
// then this type's first declared title in code order.
//
// The candidates are checked against THIS type rather than against a
// deployment-wide language list: one type may declare `en` and `ru` while
// another declares only `en`, and a resolver reading a global list would
// select `ru` for a type that has none and return an empty string.
func (t *TypeDef) TitleFor(locales ...string) string {
	for _, loc := range locales {
		if s := t.Title[loc]; s != "" {
			return s
		}
	}
	codes := make([]string, 0, len(t.Title))
	for code, s := range t.Title {
		if s != "" {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return ""
	}
	sort.Strings(codes)
	return t.Title[codes[0]]
}

// The process catalogue. New reads it for a type's status and retryability;
// a sidecar answering Declarion never sets one and Declarion fills both as
// it renders.
var (
	processCatalogueMu sync.RWMutex
	processCatalogue   Catalogue
)

// SetCatalogue installs the process catalogue. Called once at boot by
// whoever loaded the modules.
func SetCatalogue(c Catalogue) {
	processCatalogueMu.Lock()
	defer processCatalogueMu.Unlock()
	processCatalogue = c
}

func catalogue() Catalogue {
	processCatalogueMu.RLock()
	defer processCatalogueMu.RUnlock()
	return processCatalogue
}
