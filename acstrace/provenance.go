package acstrace

import (
	"slices"

	"go.opentelemetry.io/otel/attribute"
)

type Provenance struct {
	Origin   string
	SourceID string
}

func ProvenanceAttributes(items []Provenance) []attribute.KeyValue {
	origins, sources := map[string]bool{}, map[string]bool{}
	for _, item := range items {
		if item.Origin == "" {
			continue
		}
		origins[item.Origin] = true
		if item.SourceID != "" {
			sources[item.SourceID] = true
		}
	}
	values := make([]string, 0, len(origins))
	for origin := range origins {
		values = append(values, origin)
	}
	slices.Sort(values)
	var attributes []attribute.KeyValue
	if len(values) == 1 {
		attributes = append(attributes, attribute.String("acs.provenance.origin", values[0]))
	} else if len(values) > 1 {
		attributes = append(attributes, attribute.StringSlice("acs.provenance.origin", values))
	}
	if len(sources) == 1 {
		for source := range sources {
			attributes = append(attributes, attribute.String("acs.provenance.source_id", source))
		}
	}
	return attributes
}
