//go:build integration

package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const (
	sshTestImage       = "lscr.io/linuxserver/openssh-server:latest"
	sshTestContainer   = "invoke-ssh-test-container-refactored"
	sshTestPortDefault = 2224
)

func TestIntegration(t *testing.T) {
	config, cleanup := setupSSHEnvironment(t)
	defer cleanup()

	t.Logf("Connecting to %s@%s:%d...", config.User, config.Host, config.Port)

	env, err := New(WithConfig(config))
	require.NoError(t, err)
	defer env.Close()

	ctx := context.Background()

	t.Run("Run simple command", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := invoke.Command{
			Cmd:    "echo",
			Args:   []string{"hello ssh"},
			Stdout: &stdout,
		}

		res, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "hello ssh\n", stdout.String())
	})

	t.Run("Run error command", func(t *testing.T) {
		cmd := invoke.Command{
			Cmd:  "exit",
			Args: []string{"1"},
		}
		res, err := env.Run(ctx, &cmd)
		require.Error(t, err) // ExitError
		if res != nil {
			assert.Equal(t, 1, res.ExitCode)
		}
	})

	t.Run("FileTransfer", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcFile := filepath.Join(tmpDir, "local_upload.txt")
		err := os.WriteFile(srcFile, []byte("ssh-transfer-content"), 0600)
		require.NoError(t, err)

		// Note: We use /config/ folder if running in docker (linuxserver image), /tmp otherwise
		remotePath := "/tmp/invoke_ssh_upload.txt"
		if os.Getenv("SSH_TEST_HOST") == "" {
			remotePath = "/config/invoke_ssh_upload.txt"
		}

		err = env.Upload(ctx, srcFile, remotePath, invoke.WithPermissions(0644))
		require.NoError(t, err)

		// verify it arrived
		var stdout bytes.Buffer
		catCmd := invoke.Command{
			Cmd:    "cat",
			Args:   []string{remotePath},
			Stdout: &stdout,
		}
		_, err = env.Run(ctx, &catCmd)
		require.NoError(t, err)
		assert.Equal(t, "ssh-transfer-content", stdout.String())

		dstFile := filepath.Join(tmpDir, "local_download.txt")
		err = env.Download(ctx, remotePath, dstFile)
		require.NoError(t, err)

		content, err := os.ReadFile(dstFile)
		require.NoError(t, err)
		assert.Equal(t, "ssh-transfer-content", string(content))

		// Cleanup remote
		_, _ = env.Run(ctx, &invoke.Command{Cmd: "rm", Args: []string{remotePath}})

		t.Run("creates missing remote parents", func(t *testing.T) {
			remoteBase := "/tmp"
			if os.Getenv("SSH_TEST_HOST") == "" {
				remoteBase = "/config"
			}

			// Start from a clean slate to ensure parent dirs are actually created by Upload.
			remoteParentRoot := filepath.ToSlash(filepath.Join(remoteBase, "invoke-ssh-upload-parent-create"))
			_, _ = env.Run(ctx, &invoke.Command{Cmd: "rm", Args: []string{"-rf", remoteParentRoot}})

			remotePath := filepath.ToSlash(filepath.Join(remoteParentRoot, "nested", "deeper", "upload.txt"))
			err = env.Upload(ctx, srcFile, remotePath, invoke.WithPermissions(0644))
			require.NoError(t, err)

			var stdout bytes.Buffer
			catCmd := invoke.Command{
				Cmd:    "cat",
				Args:   []string{remotePath},
				Stdout: &stdout,
			}
			_, err = env.Run(ctx, &catCmd)
			require.NoError(t, err)
			assert.Equal(t, "ssh-transfer-content", stdout.String())

			// Also verify Windows-style separators are normalized correctly.
			windowsStylePath := remoteBase + "\\invoke-ssh-upload-parent-create\\win\\style\\upload.txt"
			err = env.Upload(ctx, srcFile, windowsStylePath, invoke.WithPermissions(0644))
			require.NoError(t, err)

			stdout.Reset()
			catWinCmd := invoke.Command{
				Cmd:    "cat",
				Args:   []string{filepath.ToSlash(filepath.Join(remoteBase, "invoke-ssh-upload-parent-create", "win", "style", "upload.txt"))},
				Stdout: &stdout,
			}
			_, err = env.Run(ctx, &catWinCmd)
			require.NoError(t, err)
			assert.Equal(t, "ssh-transfer-content", stdout.String())

			_, _ = env.Run(ctx, &invoke.Command{Cmd: "rm", Args: []string{"-rf", remoteParentRoot}})
		})
	})

	t.Run("Signal", func(t *testing.T) {
		// Start a long process
		cmd := invoke.Command{Cmd: "sleep", Args: []string{"10"}}
		process, err := env.Start(ctx, &cmd)
		require.NoError(t, err)
		defer process.Close()

		// Wait a bit for it to start
		time.Sleep(500 * time.Millisecond)

		// Send Kill
		err = process.Signal(os.Kill)
		require.NoError(t, err)

		// Wait for exit
		err = process.Wait()

		// SSH sessions often return -1 or signal exit code on kill
		// We just want to ensure it DOES return and doesn't hang for 10s
		if err != nil {
			var exitErr *invoke.ExitError
			if errors.As(err, &exitErr) {
				assert.NotEqual(t, 0, exitErr.ExitCode)
			}
		}
	})
	t.Run("Working Directory", func(t *testing.T) {
		var stdout bytes.Buffer
		dir := "/tmp"
		if os.Getenv("SSH_TEST_HOST") == "" {
			dir = "/config" // docker image specific
		}

		cmd := invoke.Command{
			Cmd:    "pwd",
			Dir:    dir,
			Stdout: &stdout,
		}
		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), dir)
	})

	t.Run("Environment Variables", func(t *testing.T) {
		var stdout bytes.Buffer
		cmd := invoke.Command{
			Cmd:    "sh",
			Args:   []string{"-c", "echo $INVOKE_TEST_VAR"},
			Env:    []string{"INVOKE_TEST_VAR=hello_env"},
			Stdout: &stdout,
		}
		_, err := env.Run(ctx, &cmd)
		require.NoError(t, err)
		assert.Equal(t, "hello_env\n", stdout.String())
	})
}

// setupSSHEnvironment determines if we should use an existing SSH host or spin up a Docker container.
func setupSSHEnvironment(t *testing.T) (Config, func()) {
	// Check for manual override (e.g. CI or manual testing)
	host := os.Getenv("SSH_TEST_HOST")
	if host != "" {
		portStr := os.Getenv("SSH_TEST_PORT")
		port, _ := strconv.Atoi(portStr)
		if port == 0 {
			port = 22
		}
		return Config{
			Host:               host,
			Port:               port,
			User:               os.Getenv("SSH_TEST_USER"),
			Password:           os.Getenv("SSH_TEST_PASS"),
			PrivateKeyPath:     os.Getenv("SSH_TEST_KEY_PATH"),
			Timeout:            5 * time.Second,
			InsecureSkipVerify: true,
		}, func() {}
	}

	// Fallback to Docker
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("SSH_TEST_HOST not set and docker not found in PATH")
	}

	privKey, pubKey, err := generateSSHKey()
	require.NoError(t, err)

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_rsa_test")
	err = os.WriteFile(keyPath, privKey, 0600)
	require.NoError(t, err)

	sshTestUser := "testuser"

	// Cleanup existing container if any
	_ = exec.Command("docker", "rm", "-f", sshTestContainer).Run()

	cmd := exec.Command("docker", "run", "-d",
		"--name", sshTestContainer,
		"-p", fmt.Sprintf("%d:2222", sshTestPortDefault),
		"-e", "PUID=1000",
		"-e", "PGID=1000",
		"-e", "USER_NAME="+sshTestUser,
		"-e", "PUBLIC_KEY="+string(pubKey),
		"-e", "SUDO_ACCESS=true",
		"-e", "PASSWORD_ACCESS=false",
		sshTestImage,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to start docker container: %s", out)

	// Wait for container to be inspectable
	time.Sleep(1 * time.Second)

	cleanup := func() {
		if os.Getenv("KEEP_SSH_CONTAINER") == "" {
			_ = exec.Command("docker", "rm", "-f", sshTestContainer).Run()
		}
	}

	// Use localhost and mapped port for Mac compatibility
	host = "127.0.0.1"
	port := sshTestPortDefault

	// Wait for SSH ready
	addr := fmt.Sprintf("%s:%d", host, port)
	if !waitForPort(addr, 30*time.Second) {
		logs, _ := exec.Command("docker", "logs", sshTestContainer).CombinedOutput()
		cleanup()
		t.Fatalf("SSH server never became ready at %s. Logs:\n%s", addr, logs)
	}
	// Give sshd a grace period
	time.Sleep(3 * time.Second)

	return Config{
		Host:               host,
		Port:               port,
		User:               sshTestUser,
		PrivateKeyPath:     keyPath,
		Timeout:            5 * time.Second,
		InsecureSkipVerify: true,
	}, cleanup
}

func generateSSHKey() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	privBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	pub, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(privBlock), ssh.MarshalAuthorizedKey(pub), nil
}

func waitForPort(addr string, timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
