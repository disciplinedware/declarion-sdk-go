package handlerparam

// Param is the minimal YAML wire declaration for one handler parameter.
// The parameter code is the map key in generated YAML, not a serialized field.
type Param struct {
	Name      string `yaml:"-" json:"name"`
	Type      string `yaml:"type,omitempty" json:"type,omitempty"`
	Required  bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Default   any    `yaml:"default,omitempty" json:"default,omitempty"`
	Hidden    bool   `yaml:"hidden,omitempty" json:"hidden,omitempty"`
	Sensitive bool   `yaml:"sensitive,omitempty" json:"-"`
	Enum      string `yaml:"enum,omitempty" json:"enum,omitempty"`
}

// Params preserves Go struct field order for generated YAML.
type Params []Param
