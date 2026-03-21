package lint

// Severity represents the severity level of a diagnostic.
type Severity int

const (
	Error   Severity = iota // 0
	Warning                 // 1
	Info                    // 2
)

// Diagnostic represents a single lint finding.
type Diagnostic struct {
	Severity Severity
	File     string
	Message  string
	Check    string
}

// Options controls lint behavior.
type Options struct {
	WindowsMode bool
}

// Lint analyzes the workflow in scopeDir and returns diagnostics sorted by
// severity ascending (Error first), then filename ascending, then check name
// ascending.
func Lint(scopeDir string, opts Options) ([]Diagnostic, error) {
	return nil, nil
}
