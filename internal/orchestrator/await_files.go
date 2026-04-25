package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// StageInputFiles creates the per-input subdirectory under
// <taskFolder>/inputs/<inputID>/, copies each declared display file into it
// (under its display name, or the source basename if no display name was
// given), sniffs each file's content type, and returns the resulting
// FileRecord metadata.
//
// The function is idempotent: re-running it with the same inputs leaves
// already-staged files in place rather than failing or duplicating, which
// supports daemon-restart recovery on a pending await.
//
// Source paths are interpreted strictly relative to taskFolder. Absolute
// paths, paths containing ".." segments, and symlinks that escape the task
// folder are rejected. A missing source file is reported as a descriptive
// error rather than silently skipped.
func StageInputFiles(taskFolder, inputID string, affordance parsing.FileAffordance) ([]wfstate.FileRecord, error) {
	inputDir := filepath.Join(taskFolder, "inputs", inputID)
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create input subdirectory %q: %w", inputDir, err)
	}

	if len(affordance.DisplayFiles) == 0 {
		return nil, nil
	}

	records := make([]wfstate.FileRecord, 0, len(affordance.DisplayFiles))
	for _, df := range affordance.DisplayFiles {
		rec, err := stageDisplayFile(taskFolder, inputDir, df)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

func stageDisplayFile(taskFolder, inputDir string, df parsing.DisplaySpec) (wfstate.FileRecord, error) {
	src, err := resolveDisplaySource(taskFolder, df.SourcePath)
	if err != nil {
		return wfstate.FileRecord{}, err
	}

	name := df.DisplayName
	if name == "" {
		name = filepath.Base(df.SourcePath)
	}
	dst := filepath.Join(inputDir, name)

	srcInfo, err := os.Stat(src)
	if err != nil {
		return wfstate.FileRecord{}, fmt.Errorf("stat display file %q: %w", df.SourcePath, err)
	}
	if srcInfo.IsDir() {
		return wfstate.FileRecord{}, fmt.Errorf("display file %q is a directory", df.SourcePath)
	}

	// Idempotent re-entry: a previous staging attempt may already have copied
	// the file. If a same-sized file is in place, leave it and rebuild the
	// record from the staged bytes.
	if dstInfo, statErr := os.Stat(dst); statErr == nil && !dstInfo.IsDir() && dstInfo.Size() == srcInfo.Size() {
		ct, sniffErr := sniffContentType(dst)
		if sniffErr != nil {
			return wfstate.FileRecord{}, sniffErr
		}
		return wfstate.FileRecord{Name: name, Size: dstInfo.Size(), ContentType: ct, Source: "display"}, nil
	}

	in, err := os.Open(src)
	if err != nil {
		return wfstate.FileRecord{}, fmt.Errorf("open display file %q: %w", df.SourcePath, err)
	}
	defer in.Close()

	head := make([]byte, 512)
	n, readErr := io.ReadFull(in, head)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return wfstate.FileRecord{}, fmt.Errorf("read display file %q: %w", df.SourcePath, readErr)
	}
	contentType := http.DetectContentType(head[:n])

	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return wfstate.FileRecord{}, fmt.Errorf("rewind display file %q: %w", df.SourcePath, err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return wfstate.FileRecord{}, fmt.Errorf("create staged file %q: %w", dst, err)
	}
	written, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return wfstate.FileRecord{}, fmt.Errorf("copy display file %q: %w", df.SourcePath, copyErr)
	}
	if closeErr != nil {
		return wfstate.FileRecord{}, fmt.Errorf("close staged file %q: %w", dst, closeErr)
	}

	return wfstate.FileRecord{
		Name:        name,
		Size:        written,
		ContentType: contentType,
		Source:      "display",
	}, nil
}

// resolveDisplaySource validates src as a task-folder-relative path and
// returns its absolute, symlink-resolved form. Absolute paths, paths
// containing ".." segments, and symlinks that point outside taskFolder are
// rejected.
func resolveDisplaySource(taskFolder, src string) (string, error) {
	if src == "" {
		return "", fmt.Errorf("display file source path is empty")
	}
	if filepath.IsAbs(src) || strings.HasPrefix(src, "/") || strings.HasPrefix(src, `\`) {
		return "", fmt.Errorf("display file source %q is absolute", src)
	}
	for _, seg := range strings.FieldsFunc(src, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", fmt.Errorf("display file source %q contains \"..\" segment", src)
		}
	}

	absRoot, err := filepath.Abs(taskFolder)
	if err != nil {
		return "", fmt.Errorf("resolve task folder %q: %w", taskFolder, err)
	}
	rootEval, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve task folder %q: %w", taskFolder, err)
	}

	candidate := filepath.Join(absRoot, src)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("display file %q not found in task folder", src)
		}
		return "", fmt.Errorf("resolve display file %q: %w", src, err)
	}

	rel, err := filepath.Rel(rootEval, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("display file %q escapes task folder via symlinks", src)
	}
	return resolved, nil
}

func sniffContentType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for content-type sniff: %w", path, err)
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read %q for content-type sniff: %w", path, err)
	}
	return http.DetectContentType(head[:n]), nil
}
