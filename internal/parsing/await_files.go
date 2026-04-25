package parsing

// File affordance descriptor for <await> transitions.
//
// The existing parser (parsing.go) is regex-based and only recognizes
// attribute pairs on the opening tag, so the file-affordance surface rides on
// attributes rather than nested elements.
//
// Surface syntax (one example per mode):
//
//   Slot mode (named upload slots, optional per-slot MIME allowlist):
//     <await next="NEXT.md"
//            upload_slots="resume.pdf:application/pdf,cover.pdf:application/pdf|text/plain">
//        Please upload your documents.
//     </await>
//
//   Bucket mode (open-ended uploads with constraints):
//     <await next="NEXT.md"
//            upload_bucket="true"
//            upload_max_count="5"
//            upload_max_size="10485760"
//            upload_max_total_size="52428800"
//            upload_mime="image/png,image/jpeg">
//        Attach any supporting images.
//     </await>
//
//   Display-only (workflow exposes files for the user to view):
//     <await next="NEXT.md"
//            display_files="out/report.pdf:Final Report,chart.png">
//        Please review the artifacts.
//     </await>
//
// Display files may also be combined with slot or bucket mode.
//
// Attribute grammar:
//   upload_slots          := slot ("," slot)*
//   slot                  := name | name ":" mime ("|" mime)*
//   upload_bucket         := "true" | "false"
//   upload_max_count      := positive integer
//   upload_max_size       := positive integer (bytes per file)
//   upload_max_total_size := positive integer (bytes for the whole submission)
//   upload_mime           := mime ("," mime)*
//   display_files         := entry ("," entry)*
//   entry                 := source-path | source-path ":" display-label

import (
	"fmt"
	"strconv"
	"strings"
)

// Mode classifies the file affordance an <await> declares.
//
// Display files are independent of the upload mode: a slot- or bucket-mode
// await may also carry display files. ModeDisplayOnly is reserved for awaits
// that declare display files but no upload affordance.
type Mode int

const (
	// ModeTextOnly is the zero value: the await accepts only a text response.
	ModeTextOnly Mode = iota
	// ModeSlot indicates the await declares one or more named upload slots.
	ModeSlot
	// ModeBucket indicates the await declares an open-ended upload bucket
	// with constraints.
	ModeBucket
	// ModeDisplayOnly indicates the await exposes display files but does not
	// accept uploads.
	ModeDisplayOnly
)

// SlotSpec describes a named upload slot.
type SlotSpec struct {
	Name string
	MIME []string
}

// BucketSpec describes the constraints on an open-ended upload bucket.
// Zero-valued numeric fields mean "unconstrained" and are interpreted by the
// upload handler against its own server-side defaults.
type BucketSpec struct {
	MaxCount       int
	MaxSizePerFile int64
	MaxTotalSize   int64
	MIME           []string
}

// DisplaySpec describes a file the workflow wants to expose to the user
// alongside the prompt. SourcePath is interpreted relative to the requesting
// agent's task folder; DisplayName is the file name shown to the user (and
// used as the staged filename inside the per-input subdirectory). An empty
// DisplayName means "use the basename of SourcePath".
type DisplaySpec struct {
	SourcePath  string
	DisplayName string
}

// FileAffordance is the parsed file-related descriptor for an <await> tag.
// The zero value represents a text-only await.
type FileAffordance struct {
	Mode         Mode
	Slots        []SlotSpec
	Bucket       BucketSpec
	DisplayFiles []DisplaySpec
}

// ParseFileAffordance extracts the file affordance from <await> attributes.
// Unrelated attributes (e.g. next, timeout) are ignored. The returned
// affordance is normalized: MIME types are trimmed and lowercased, leading
// and trailing whitespace is removed from names. An empty / zero-value
// FileAffordance is returned for awaits that declare no file attributes.
func ParseFileAffordance(attrs map[string]string) (FileAffordance, error) {
	var fa FileAffordance

	hasSlots := false
	if v, ok := attrs["upload_slots"]; ok {
		hasSlots = true
		slots, err := parseSlots(v)
		if err != nil {
			return FileAffordance{}, err
		}
		fa.Slots = slots
	}

	hasBucket := false
	if v, ok := attrs["upload_bucket"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			hasBucket = true
		case "false":
			hasBucket = false
		default:
			return FileAffordance{}, fmt.Errorf(
				"upload_bucket must be \"true\" or \"false\", got %q", v)
		}
	}

	if hasSlots && hasBucket {
		return FileAffordance{}, fmt.Errorf(
			"upload_slots and upload_bucket are mutually exclusive; pick one mode")
	}

	bucketSubAttrs := []string{
		"upload_max_count",
		"upload_max_size",
		"upload_max_total_size",
		"upload_mime",
	}
	if !hasBucket {
		for _, k := range bucketSubAttrs {
			if _, ok := attrs[k]; ok {
				return FileAffordance{}, fmt.Errorf(
					"%s requires upload_bucket=\"true\"", k)
			}
		}
	} else {
		bucket, err := parseBucket(attrs)
		if err != nil {
			return FileAffordance{}, err
		}
		fa.Bucket = bucket
	}

	if v, ok := attrs["display_files"]; ok {
		ds, err := parseDisplayFiles(v)
		if err != nil {
			return FileAffordance{}, err
		}
		fa.DisplayFiles = ds
	}

	switch {
	case hasSlots:
		fa.Mode = ModeSlot
	case hasBucket:
		fa.Mode = ModeBucket
	case len(fa.DisplayFiles) > 0:
		fa.Mode = ModeDisplayOnly
	default:
		fa.Mode = ModeTextOnly
	}

	return fa, nil
}

func parseSlots(raw string) ([]SlotSpec, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || (len(parts) == 1 && strings.TrimSpace(parts[0]) == "") {
		return nil, fmt.Errorf("upload_slots must list at least one slot name")
	}

	seen := make(map[string]struct{}, len(parts))
	slots := make([]SlotSpec, 0, len(parts))
	for _, p := range parts {
		entry := strings.TrimSpace(p)
		if entry == "" {
			return nil, fmt.Errorf("upload_slots: empty slot name")
		}

		var name string
		var mimes []string
		if idx := strings.Index(entry, ":"); idx >= 0 {
			name = strings.TrimSpace(entry[:idx])
			mimePart := entry[idx+1:]
			ms, err := parseMIMEList(mimePart, "|", "upload_slots")
			if err != nil {
				return nil, err
			}
			mimes = ms
		} else {
			name = entry
		}

		if err := validateSlotName(name); err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("upload_slots: duplicate slot name %q", name)
		}
		seen[name] = struct{}{}
		slots = append(slots, SlotSpec{Name: name, MIME: mimes})
	}
	return slots, nil
}

func parseBucket(attrs map[string]string) (BucketSpec, error) {
	var b BucketSpec

	if v, ok := attrs["upload_max_count"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			return BucketSpec{}, fmt.Errorf(
				"upload_max_count must be a positive integer, got %q", v)
		}
		b.MaxCount = n
	}
	if v, ok := attrs["upload_max_size"]; ok {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n <= 0 {
			return BucketSpec{}, fmt.Errorf(
				"upload_max_size must be a positive integer, got %q", v)
		}
		b.MaxSizePerFile = n
	}
	if v, ok := attrs["upload_max_total_size"]; ok {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n <= 0 {
			return BucketSpec{}, fmt.Errorf(
				"upload_max_total_size must be a positive integer, got %q", v)
		}
		b.MaxTotalSize = n
	}
	if v, ok := attrs["upload_mime"]; ok {
		ms, err := parseMIMEList(v, ",", "upload_mime")
		if err != nil {
			return BucketSpec{}, err
		}
		b.MIME = ms
	}
	return b, nil
}

func parseMIMEList(raw, sep, attr string) ([]string, error) {
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.ToLower(strings.TrimSpace(p))
		if v == "" {
			return nil, fmt.Errorf("%s: empty MIME entry", attr)
		}
		out = append(out, v)
	}
	return out, nil
}

func parseDisplayFiles(raw string) ([]DisplaySpec, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || (len(parts) == 1 && strings.TrimSpace(parts[0]) == "") {
		return nil, fmt.Errorf("display_files must list at least one entry")
	}

	out := make([]DisplaySpec, 0, len(parts))
	for _, p := range parts {
		entry := strings.TrimSpace(p)
		if entry == "" {
			return nil, fmt.Errorf("display_files: empty entry")
		}

		var src, label string
		if idx := strings.Index(entry, ":"); idx >= 0 {
			src = strings.TrimSpace(entry[:idx])
			label = strings.TrimSpace(entry[idx+1:])
		} else {
			src = entry
		}

		if err := validateDisplaySource(src); err != nil {
			return nil, err
		}
		if label != "" {
			if err := validateDisplayLabel(label); err != nil {
				return nil, err
			}
		}
		out = append(out, DisplaySpec{SourcePath: src, DisplayName: label})
	}
	return out, nil
}

func validateSlotName(name string) error {
	if name == "" {
		return fmt.Errorf("upload_slots: empty slot name")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("upload_slots: slot name %q contains path separator", name)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("upload_slots: slot name %q contains null byte", name)
	}
	if hasControlChar(name) {
		return fmt.Errorf("upload_slots: slot name %q contains control character", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("upload_slots: slot name %q has a leading dot", name)
	}
	return nil
}

func validateDisplaySource(src string) error {
	if src == "" {
		return fmt.Errorf("display_files: empty source path")
	}
	if strings.ContainsRune(src, 0) {
		return fmt.Errorf("display_files: source path %q contains null byte", src)
	}
	if hasControlChar(src) {
		return fmt.Errorf("display_files: source path %q contains control character", src)
	}
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "\\") {
		return fmt.Errorf("display_files: source path %q is absolute", src)
	}
	for _, seg := range strings.FieldsFunc(src, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("display_files: source path %q contains \"..\" segment", src)
		}
	}
	return nil
}

func validateDisplayLabel(label string) error {
	if strings.ContainsRune(label, 0) {
		return fmt.Errorf("display_files: display name %q contains null byte", label)
	}
	if hasControlChar(label) {
		return fmt.Errorf("display_files: display name %q contains control character", label)
	}
	if strings.ContainsAny(label, "/\\") {
		return fmt.Errorf("display_files: display name %q contains path separator", label)
	}
	if strings.HasPrefix(label, ".") {
		return fmt.Errorf("display_files: display name %q has a leading dot", label)
	}
	return nil
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
