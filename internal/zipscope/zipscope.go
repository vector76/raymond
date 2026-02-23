// Package zipscope provides access to workflow scopes stored inside zip archives.
//
// A "zip scope" is a .zip file used in place of a directory to bundle all
// workflow state files into a single distributable artifact. The functions in
// this package mirror the directory-based equivalents used elsewhere (os.ReadFile,
// os.Stat, os.ReadDir) but operate on the zip archive's virtual file system.
//
// Detection: IsZipScope returns true when the scope path ends in ".zip"
// (case-insensitive). All other paths are treated as plain directories.
package zipscope

import (
	"archive/zip"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// ZipFileNotFoundError is returned by ReadText when the requested file does
// not exist inside the zip archive.
type ZipFileNotFoundError struct {
	ZipPath  string
	Filename string
}

func (e *ZipFileNotFoundError) Error() string {
	return fmt.Sprintf("file not found in zip: %s (in %s)", e.Filename, e.ZipPath)
}

// IsZipScope reports whether scopeDir refers to a zip archive (i.e. ends in
// ".zip", case-insensitive). All other paths are treated as plain directories.
func IsZipScope(scopeDir string) bool {
	return strings.ToLower(filepath.Ext(scopeDir)) == ".zip"
}

// ReadText reads filename from the zip archive at zipPath and returns its
// contents as a UTF-8 string. Returns *ZipFileNotFoundError when the file does
// not exist inside the archive.
func ReadText(zipPath, filename string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == filename {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("failed to open %s in zip %s: %w", filename, zipPath, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return "", fmt.Errorf("failed to read %s from zip %s: %w", filename, zipPath, err)
			}
			return string(data), nil
		}
	}
	return "", &ZipFileNotFoundError{ZipPath: zipPath, Filename: filename}
}

// FileExists reports whether filename exists inside the zip archive at zipPath.
// Returns false for any error (including invalid zip or missing file).
func FileExists(zipPath, filename string) bool {
	_, err := ReadText(zipPath, filename)
	return err == nil
}

// ListFiles returns the names of all non-directory entries in the zip archive
// at zipPath.
func ListFiles(zipPath string) ([]string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	var files []string
	for _, f := range r.File {
		if !f.FileInfo().IsDir() {
			files = append(files, f.Name)
		}
	}
	return files, nil
}
