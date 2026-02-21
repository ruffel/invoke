// Package fileutil provides shared file-transfer utilities for invoke providers.
//
// This package is intended to be used by provider implementations to avoid
// duplicating common patterns like progress reporting, context cancellation
// checking, and path traversal validation.
package fileutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
)

// ProgressReader wraps an io.Reader to report progress via an invoke.ProgressFunc.
// Total should be set to the known total size for percentage-based progress reporting,
// or 0 if unknown.
type ProgressReader struct {
	io.Reader

	Total   int64
	Current int64
	Fn      invoke.ProgressFunc
}

// Read reads from the underlying reader and reports progress.
func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.Current += int64(n)
		if pr.Fn != nil {
			pr.Fn(pr.Current, pr.Total)
		}
	}

	return n, err
}

// ContextReader wraps an io.Reader to check for context cancellation
// before each Read call. This allows long-running io.Copy operations
// to be interrupted by context cancellation.
type ContextReader struct {
	Ctx    context.Context //nolint:containedctx
	Reader io.Reader
}

// Read checks for context cancellation before delegating to the underlying reader.
func (cr *ContextReader) Read(p []byte) (int, error) {
	if cr.Ctx.Err() != nil {
		return 0, cr.Ctx.Err()
	}

	return cr.Reader.Read(p)
}

// CheckPathTraversal validates that target is a child of root using local filesystem
// path conventions (filepath.Abs, os.PathSeparator). Returns an error if target
// escapes the root directory (ZipSlip protection).
func CheckPathTraversal(root, target string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("illegal file path: cannot resolve root %s: %w", root, err)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("illegal file path: cannot resolve target %s: %w", target, err)
	}

	if absRoot == absTarget {
		return nil
	}

	if !strings.HasPrefix(absTarget, absRoot+string(os.PathSeparator)) {
		return fmt.Errorf("illegal file path: %s is not within %s", target, root)
	}

	return nil
}

// CheckRemotePathTraversal validates that target is a child of root using forward-slash
// path conventions (path.Clean, "/"). Use this for remote Unix-like paths where
// filepath operations would use the wrong separator on Windows hosts.
func CheckRemotePathTraversal(root, target string) error {
	cleanRoot := path.Clean(root)
	cleanTarget := path.Clean(target)

	if cleanRoot == cleanTarget {
		return nil
	}

	if !strings.HasPrefix(cleanTarget, cleanRoot+"/") {
		return fmt.Errorf("illegal remote file path: %s is not within %s", target, root)
	}

	return nil
}
