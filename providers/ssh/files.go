package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"github.com/ruffel/invoke"
)

// Upload copies a local file/dir to the remote path using SFTP.
func (e *Environment) Upload(ctx context.Context, localPath, remotePath string, opts ...invoke.FileOption) error {
	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return errors.New("environment closed")
	}
	// We assume client is active
	client := e.client
	e.mu.Unlock()

	cfg := invoke.DefaultFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("failed to create sftp client: %w", err)
	}

	defer func() { _ = sftpClient.Close() }()

	// Check local info
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return e.uploadDir(ctx, sftpClient, localPath, remotePath, cfg)
	}

	mode := info.Mode()
	if cfg.Permissions != 0 {
		mode = cfg.Permissions
	}

	return e.uploadFile(ctx, sftpClient, localPath, remotePath, mode, cfg.Progress)
}

func (e *Environment) uploadDir(ctx context.Context, client *sftp.Client, localBase, remoteBase string, cfg invoke.FileConfig) error {
	return filepath.Walk(localBase, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(localBase, path)
		if err != nil {
			return err
		}

		remotePath := pathpkg.Join(remoteBase, relPath)
		// Convert to forward slashes for remote linux paths (path.Join mostly matches but ensuring cleanliness)
		remotePath = strings.ReplaceAll(remotePath, "\\", "/")

		if info.IsDir() {
			err := client.MkdirAll(remotePath)
			if err != nil {
				return err
			}

			if cfg.Permissions != 0 {
				_ = client.Chmod(remotePath, cfg.Permissions)
			}

			return nil
		}

		mode := info.Mode()
		if cfg.Permissions != 0 {
			mode = cfg.Permissions
		}

		return e.uploadFile(ctx, client, path, remotePath, mode, cfg.Progress)
	})
}

func (e *Environment) uploadFile(ctx context.Context, client *sftp.Client, localPath, remotePath string, mode os.FileMode, progress invoke.ProgressFunc) error {
	// Check context
	if ctx.Err() != nil {
		return ctx.Err()
	}

	src, err := os.Open(localPath)
	if err != nil {
		return err
	}

	defer func() { _ = src.Close() }()

	// Get size for progress
	var size int64
	if info, err := src.Stat(); err == nil {
		size = info.Size()
	}

	dst, err := client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("failed to create remote file %q: %w", remotePath, err)
	}

	defer func() { _ = dst.Close() }()

	if err := client.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("failed to chmod remote file: %w", err)
	}

	var reader io.Reader = src
	if progress != nil {
		reader = &progressReader{Reader: src, total: size, fn: progress}
	}

	_, err = io.Copy(dst, reader)

	return err
}

// Download copies a remote file/dir to the local path using SFTP.
func (e *Environment) Download(ctx context.Context, remotePath, localPath string, opts ...invoke.FileOption) error {
	// Options like permissions could apply to local files too
	cfg := invoke.DefaultFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()

		return errors.New("environment closed")
	}

	client := e.client
	e.mu.Unlock()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("failed to create sftp client: %w", err)
	}

	defer func() { _ = sftpClient.Close() }()

	info, err := sftpClient.Stat(remotePath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return e.downloadDir(ctx, sftpClient, remotePath, localPath, cfg.Progress)
	}

	return e.downloadFile(ctx, sftpClient, remotePath, localPath, info.Mode(), cfg.Progress)
}

func (e *Environment) downloadDir(ctx context.Context, client *sftp.Client, remoteBase, localBase string, progress invoke.ProgressFunc) error {
	walker := client.Walk(remoteBase)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}

		path := walker.Path()

		relPath, err := filepath.Rel(remoteBase, path)
		if err != nil {
			continue
		}

		localPath := filepath.Join(localBase, relPath)
		info := walker.Stat()

		if info.IsDir() {
			err := os.MkdirAll(localPath, info.Mode())
			if err != nil {
				return err
			}

			continue
		}

		if err := e.downloadFile(ctx, client, path, localPath, info.Mode(), progress); err != nil {
			return err
		}
	}
	// Force chmod if mode is specific
	// sftp.Create doesn't always guarantee mode, especially with umask.
	// Explicit chmod is safer.
	return nil
}

func (e *Environment) downloadFile(ctx context.Context, client *sftp.Client, remotePath, localPath string, mode os.FileMode, progress invoke.ProgressFunc) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	src, err := client.Open(remotePath)
	if err != nil {
		return err
	}

	defer func() { _ = src.Close() }()

	var size int64
	if info, err := src.Stat(); err == nil {
		size = info.Size()
	}

	// Ensure parent exists
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}

	dst, err := os.Create(localPath)
	if err != nil {
		return err
	}

	defer func() { _ = dst.Close() }()

	if err := os.Chmod(localPath, mode); err != nil {
		return fmt.Errorf("failed to chmod local file: %w", err)
	}

	var reader io.Reader = src
	if progress != nil {
		reader = &progressReader{Reader: src, total: size, fn: progress}
	}

	_, err = io.Copy(dst, reader)

	return err
}

type progressReader struct {
	io.Reader

	total   int64
	current int64
	fn      invoke.ProgressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.current += int64(n)
		if pr.fn != nil {
			pr.fn(pr.current, pr.total)
		}
	}

	return n, err
}
