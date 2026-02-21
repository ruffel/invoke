package docker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/fileutil"
)

// Upload copies a local file/dir to the remote path using Container tools.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.FileOption) error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return fmt.Errorf("cannot upload files: %w", invoke.ErrEnvironmentClosed)
	}

	e.mu.Unlock()

	cfg := invoke.DefaultFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// Check local info
	if _, err := os.Stat(localPath); err != nil {
		return err
	}

	// Normalize and split the remote path. We use the container root as the base
	// for CopyToContainer and include the relative path in the tar stream.
	// This allows the Docker daemon's tar extractor to create any missing intermediate directories.
	remotePath = strings.ReplaceAll(remotePath, "\\", "/")
	containerRoot := "/"

	if e.TargetOS() == invoke.OSWindows {
		if len(remotePath) >= 3 && remotePath[1] == ':' && (remotePath[2] == '/' || remotePath[2] == '\\') {
			containerRoot = remotePath[:3] // e.g., "C:/"
			remotePath = remotePath[3:]
		} else {
			containerRoot = "C:/"
			remotePath = strings.TrimPrefix(remotePath, "/")
		}
	} else {
		remotePath = strings.TrimPrefix(remotePath, "/")
	}

	tarStream := tarArchive(localPath, remotePath)

	defer func() { _ = tarStream.Close() }()

	var reader io.Reader = tarStream
	if cfg.Progress != nil {
		reader = &fileutil.ProgressReader{Reader: tarStream, Total: 0, Fn: cfg.Progress}
	}

	options := container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: true,
	}

	err := e.client.CopyToContainer(ctx, e.config.ContainerID, containerRoot, reader, options)
	if err != nil {
		return fmt.Errorf("failed to copy to container: %w", err)
	}

	return nil
}

// Download copies a remote file/dir to the local path.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.FileOption) error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return fmt.Errorf("cannot download files: %w", invoke.ErrEnvironmentClosed)
	}

	e.mu.Unlock()

	cfg := invoke.DefaultFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	reader, _, err := e.client.CopyFromContainer(ctx, e.config.ContainerID, remotePath)
	if err != nil {
		return fmt.Errorf("failed to copy from container: %w", err)
	}

	defer func() { _ = reader.Close() }()

	var r io.Reader = reader
	if cfg.Progress != nil {
		r = &fileutil.ProgressReader{Reader: reader, Total: 0, Fn: cfg.Progress}
	}

	return untar(r, localPath)
}

func tarArchive(src, destName string) io.ReadCloser {
	r, w := io.Pipe()

	go func() {
		defer func() { _ = w.Close() }()

		tw := tar.NewWriter(w)

		defer func() { _ = tw.Close() }()

		_, err := os.Stat(src) // fixed: was _, err
		if err != nil {
			_ = w.CloseWithError(err)

			return
		}

		baseDir := filepath.Dir(src)

		err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			return writeTarEntry(tw, path, info, err, src, destName, baseDir)
		})
		if err != nil {
			_ = w.CloseWithError(err)
		}
	}()

	return r
}

func writeTarEntry(tw *tar.Writer, path string, info os.FileInfo, err error, src, destName, baseDir string) error {
	if err != nil {
		return err
	}

	// Docker expects relative paths in tar
	relPath, err := filepath.Rel(baseDir, path)
	if err != nil {
		return err
	}

	// If this matches the root source file, rename it to destName
	if path == src {
		relPath = destName
	} else if destName != "" && strings.HasPrefix(path, src) {
		// If we are renaming a directory, we need to rewrite children paths too
		// But Upload() mostly targets single files or matching hierarchies.
		// For now, let's keep it simple: strict renaming only applies if src is a file
		// or if we are renaming the root dir.
		// If src is dir, relPath is "srcBasename/child". We want "destName/child".
		// baseDir is parent(src).
		// relPath is srcBasename/...
		// We want to replace srcBasename with destName.
		srcBase := filepath.Base(src)
		if strings.HasPrefix(relPath, srcBase) {
			relPath = strings.Replace(relPath, srcBase, destName, 1)
		}
	}

	// Windows path fix
	headerName := strings.ReplaceAll(relPath, "\\", "/")

	header, err := tar.FileInfoHeader(info, headerName)
	if err != nil {
		return err
	}

	header.Name = headerName

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	if !info.IsDir() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}

		defer func() { _ = f.Close() }()

		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
	}

	return nil
}

func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)

	// Check first entry to determine mode
	firstHeader, err := tr.Next()
	if err == io.EOF {
		return nil
	}

	if err != nil {
		return err
	}

	// Optimization: If root is a file, extract directly
	if firstHeader.Typeflag == tar.TypeReg {
		dir := filepath.Dir(dst)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		f, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(firstHeader.Mode))
		if err != nil {
			return err
		}

		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()

			return err
		}

		return f.Close()
	}

	// Process first header
	if err := extractEntry(dst, firstHeader, tr); err != nil {
		return err
	}

	// Process remaining
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		if err := extractEntry(dst, header, tr); err != nil {
			return err
		}
	}

	return nil
}

func extractEntry(dstRoot string, header *tar.Header, tr *tar.Reader) error {
	// Security: prevent ZipSlip
	target := filepath.Join(dstRoot, header.Name)

	if err := fileutil.CheckPathTraversal(dstRoot, target); err != nil {
		return fmt.Errorf("illegal file path in tar: %s", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		err := os.MkdirAll(target, 0o755)
		if err != nil {
			return err
		}
	case tar.TypeReg:
		dir := filepath.Dir(target)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
		if err != nil {
			return err
		}

		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()

			return err
		}

		return f.Close()
	}

	return nil
}
