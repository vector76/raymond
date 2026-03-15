package specifier

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/vector76/raymond/internal/prompts"
	"github.com/vector76/raymond/internal/registry"
	"github.com/vector76/raymond/internal/zipscope"
)

// Fetcher retrieves a remote zip file by URL and expected hash, returning the
// local filesystem path to the cached copy.
type Fetcher func(url, hash string) (localPath string, err error)

// ResolveFromURL resolves a relative cross-workflow specifier in URL space,
// using scopeURL as the base URL of the calling workflow's zip.
//
// Steps:
//  1. Reject absolute URL specifiers (rawSpecifier itself is a remote URL).
//  2. Split at ".zip/" to separate the zip-relative part from an inner state name.
//  3. Resolve the zip-relative specifier against scopeURL (with trailing-slash fix).
//  4. Verify the resolved URL is a remote workflow URL (mixed-scope check).
//  5. Extract and validate the SHA256 hash from the resolved URL filename.
//  6. Fetch the zip via the Fetcher callback.
//  7. Resolve entry point (whole-zip or inner-component).
//  8. Derive Abbrev from the URL filename stem (not the local cache path).
func ResolveFromURL(rawSpecifier, scopeURL string, fetch Fetcher) (Resolution, error) {
	// Step 1: reject absolute URL specifiers.
	if registry.IsRemoteWorkflowURL(rawSpecifier) {
		return Resolution{}, fmt.Errorf("absolute URL specifiers are not supported as cross-workflow targets: %q", rawSpecifier)
	}

	// Step 2: split at ".zip/" (case-insensitive) to get zip part and inner state.
	zipPart := rawSpecifier
	innerState := ""
	lowerSpec := strings.ToLower(rawSpecifier)
	if idx := strings.Index(lowerSpec, ".zip/"); idx >= 0 {
		zipPart = rawSpecifier[:idx+4]   // up to and including ".zip"
		innerState = rawSpecifier[idx+5:] // remainder after ".zip/"
	}

	// Step 3: URL-space resolution with trailing-slash fix.
	base, err := url.Parse(scopeURL)
	if err != nil {
		return Resolution{}, fmt.Errorf("cannot parse scope URL %q: %w", scopeURL, err)
	}
	// Append "/" to base path so that ResolveReference treats scopeURL as a
	// directory, not a file. Without this, "../sibling.zip" from
	// "https://host/wfs/wf1.zip" would strip "wf1.zip" and resolve relative to
	// "https://host/wfs/", but Go's RFC 3986 §5.2.2 implementation strips the
	// last segment of the base path first — appending "/" makes the base path
	// "https://host/wfs/wf1.zip/" so the resolution is correct.
	base.Path = base.Path + "/"

	ref, err := url.Parse(zipPart)
	if err != nil {
		return Resolution{}, fmt.Errorf("cannot parse specifier %q as URL reference: %w", zipPart, err)
	}
	resolved := base.ResolveReference(ref)

	// Step 4: mixed-scope constraint — resolved URL must be a remote workflow URL.
	resolvedStr := resolved.String()
	if !registry.IsRemoteWorkflowURL(resolvedStr) {
		return Resolution{}, fmt.Errorf("specifier %q resolves to non-remote URL %q; cross-workflow URL specifiers must stay in URL space", rawSpecifier, resolvedStr)
	}

	// Step 5: extract and validate hash from resolved URL filename.
	hash, err := registry.ValidateRemoteURL(resolvedStr)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolved URL %q: %w", resolvedStr, err)
	}

	// Step 6: fetch (or cache-hit) the zip file.
	localPath, err := fetch(resolvedStr, hash)
	if err != nil {
		return Resolution{}, fmt.Errorf("fetching %q: %w", resolvedStr, err)
	}

	// Step 8 (computed early for reuse): Abbrev from URL filename stem, not local path.
	urlFilename := path.Base(resolved.Path)
	stem := strings.TrimSuffix(urlFilename, path.Ext(urlFilename))
	ab := abbrev(stem)

	// Step 7: resolve entry point.
	var res Resolution
	if innerState == "" {
		res, err = resolveZip(localPath)
		if err != nil {
			return Resolution{}, err
		}
		res.Abbrev = ab
	} else {
		if _, err := zipscope.DetectLayout(localPath); err != nil {
			return Resolution{}, fmt.Errorf("invalid zip %q: %w", localPath, err)
		}
		entryPoint, err := prompts.ResolveState(localPath, innerState)
		if err != nil {
			return Resolution{}, fmt.Errorf("cannot resolve state %q in %s: %w", innerState, localPath, err)
		}
		res = Resolution{
			ScopeDir:   localPath,
			EntryPoint: entryPoint,
			Abbrev:     ab,
		}
	}

	res.ScopeURL = resolvedStr
	return res, nil
}
