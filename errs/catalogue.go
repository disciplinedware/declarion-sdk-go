package errs

import (
	"fmt"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// LocalizedString is a sentence per language code. A bare scalar is read as
// English, as everywhere else in the DSL.
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

// TypeDef is one declared class of failure. Status and Retryable are the
// TYPE's and no occurrence overrides them; Title is a complete sentence with
// nothing substituted into it.
type TypeDef struct {
	Status int `yaml:"status" json:"status"`
	// Can the same call succeed later. NOT whether repeating is safe, which
	// is idempotency.
	Retryable bool            `yaml:"retryable" json:"retryable"`
	Title     LocalizedString `yaml:"title" json:"title"`
	// Read by the call-site gate; nothing enforces it at runtime, because
	// failing loudly while reporting a failure is the wrong direction.
	Fields map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`
	// A deprecated type keeps its declaration so a consumer branching on it
	// still parses; it simply stops being raised.
	Deprecated bool `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
}

// Catalogue is the merge of every loaded module's error types. One per
// process; two processes agree only if given the same ordered module set.
type Catalogue map[string]*TypeDef

func (c Catalogue) Lookup(code string) (*TypeDef, bool) {
	def, ok := c[code]
	return def, ok && def != nil
}

// TitleFor returns the first non-blank title among the candidates, then this
// type's first declared title in code order.
//
// Candidates are checked against THIS type, not a deployment-wide language
// list: one type may declare en and ru where another declares only en, and a
// global list would select ru for the second and return "".
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

var (
	processMu            sync.RWMutex
	processCat           Catalogue
	processDefaultLocale string
)

// SetCatalogue installs the process catalogue and the deployment's fallback
// language. Called by whoever loaded the modules, at boot and on hot reload.
func SetCatalogue(c Catalogue, defaultLocale string) {
	processMu.Lock()
	defer processMu.Unlock()
	processCat = c
	processDefaultLocale = defaultLocale
}

// ProcessRenderContext fills the process-wide half of a RenderContext, so a
// boundary passes only what it alone knows.
func ProcessRenderContext(locale, instance string, maxBytes int) RenderContext {
	processMu.RLock()
	defer processMu.RUnlock()
	return RenderContext{
		Catalogue:     processCat,
		Locale:        locale,
		DefaultLocale: processDefaultLocale,
		Instance:      instance,
		MaxBytes:      maxBytes,
	}
}

// Declared answers whether the process catalogue carries this code.
func Declared(code string) bool {
	_, ok := catalogue().Lookup(code)
	return ok
}

func catalogue() Catalogue {
	processMu.RLock()
	defer processMu.RUnlock()
	return processCat
}
