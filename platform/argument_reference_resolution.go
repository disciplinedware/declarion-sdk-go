package platform

// ArgsReferenceResolution carries resolved parameters and their bounded JSON cost.
type ArgsReferenceResolution struct {
	Params                  map[string]any
	DecodedMemoryBytes      int64
	DecodedMemoryLimitBytes int64
}
