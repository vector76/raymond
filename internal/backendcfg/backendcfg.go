// Package backendcfg defines the BackendSpec and BackendOptions types used by
// workflow manifests and YAML scope files to declare which agent backend a
// workflow runs against and what backend-specific options apply.
//
// This package has no raymond-internal dependencies so it can be safely
// imported by both internal/manifest and internal/yamlscope without creating
// an import cycle.
package backendcfg

// BackendOptions holds pi-specific options declared under backend.options.
// All fields are optional; their zero values mean "omit the corresponding flag".
type BackendOptions struct {
	Provider       string   `yaml:"provider"`
	Thinking       string   `yaml:"thinking"`
	Tools          []string `yaml:"tools"`
	NoBuiltinTools bool     `yaml:"no_builtin_tools"`
	NoTools        bool     `yaml:"no_tools"`
	NoExtensions   bool     `yaml:"no_extensions"`
	NoSkills       bool     `yaml:"no_skills"`
	Extensions     []string `yaml:"extensions"`
	Skills         []string `yaml:"skills"`
	SessionDir     string   `yaml:"session_dir"`
}

// BackendSpec describes the agent backend declared in a workflow manifest or
// YAML scope file. It supports two YAML forms:
//
//	backend: pi                          # bare string (Name only)
//	backend:                             # structured form
//	  name: pi
//	  options: { ... }
//
// When the backend field is absent, BackendSpec is the zero value and Name is
// "". Callers interpret that as "use the Claude default".
type BackendSpec struct {
	Name    string
	Options BackendOptions
}

// UnmarshalYAML implements yaml.Unmarshaler so that both YAML forms decode
// into the same BackendSpec type.
func (b *BackendSpec) UnmarshalYAML(unmarshal func(any) error) error {
	// Try bare string first.
	var name string
	if err := unmarshal(&name); err == nil {
		b.Name = name
		return nil
	}
	// Try structured form.
	var structured struct {
		Name    string         `yaml:"name"`
		Options BackendOptions `yaml:"options"`
	}
	if err := unmarshal(&structured); err != nil {
		return err
	}
	b.Name = structured.Name
	b.Options = structured.Options
	return nil
}
