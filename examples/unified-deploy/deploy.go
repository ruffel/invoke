// Package main demonstrates the invoke library's "write once, run anywhere"
// deployment capabilities across local, SSH, and Docker environments.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ruffel/invoke"
)

const (
	remoteAppDir = "/tmp/invoke-app"
	remoteFile   = "version.txt"
)

// Deploy runs the full deployment pipeline.
func Deploy(ctx context.Context, env invoke.Environment) error {
	LogStep("1. Pre-flight Check")

	if err := runPreFlightCheck(ctx, env); err != nil {
		return fmt.Errorf("pre-flight failed: %w", err)
	}

	LogStep("2. Local Build (Simulation)")

	if err := simulateBuild(ctx, env); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	LogStep("3. Backup")

	if err := backupExistingApp(ctx, env); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	LogStep("4. Staging Artifacts")

	localArtifact, err := generateArtifact()
	if err != nil {
		return fmt.Errorf("staging failed: %w", err)
	}

	defer func() { _ = os.Remove(localArtifact) }()

	LogStep("5. Upload to Target")

	if err := uploadApp(ctx, env, localArtifact); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	LogStep("6. Verification (Remote Cat)")

	if err := verifyDeployment(ctx, env); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	LogStep("7. Round-trip Check (Download)")

	if err := downloadAndVerify(ctx, env, localArtifact); err != nil {
		return fmt.Errorf("round-trip check failed: %w", err)
	}

	return nil
}

// LogStep prints a styled step header to stdout.
func LogStep(msg string) {
	fmt.Println(stepStyle.Render(msg))
}

func runPreFlightCheck(ctx context.Context, env invoke.Environment) error {
	cmd := &invoke.Command{Cmd: "uname", Args: []string{"-a"}}

	res, err := invoke.NewExecutor(env).RunBuffered(ctx, cmd)
	if err != nil {
		return err
	}

	fmt.Println(infoStyle.Render("Target System: " + string(res.Stdout)))

	return nil
}

func simulateBuild(ctx context.Context, env invoke.Environment) error {
	exec := invoke.NewExecutor(env)

	// Use single quotes in the script to avoid escaping issues over SSH
	script := `echo 'Dependency resolution...'; sleep 0.5; echo 'Compiling core modules...'; sleep 0.5; echo 'Linking binaries...'; sleep 0.5; echo 'Build successful!'`
	cmd := env.TargetOS().ShellCommand(script)

	// Use RunLineStream to show real-time progress
	err := exec.RunLineStream(ctx, cmd, func(line string) {
		fmt.Printf("   %s\n", line)
	})

	return err
}

func backupExistingApp(ctx context.Context, env invoke.Environment) error {
	checkCmd := &invoke.Command{Cmd: "test", Args: []string{"-d", remoteAppDir}}
	res, err := env.Run(ctx, checkCmd)

	if err != nil || res.ExitCode != 0 {
		fmt.Println(infoStyle.Render("No existing app found, skipping backup."))

		return nil //nolint:nilerr // Graceful degradation: assume no app if check fails
	}

	fmt.Println(infoStyle.Render("Found existing app, backing up..."))

	backupPath := fmt.Sprintf("%s.bak.%d", remoteAppDir, time.Now().Unix())

	mvCmd := &invoke.Command{Cmd: "mv", Args: []string{remoteAppDir, backupPath}}
	if _, err := env.Run(ctx, mvCmd); err != nil {
		return err
	}

	return nil
}

func generateArtifact() (string, error) {
	version := fmt.Sprintf("v1.0.0-%d", time.Now().Unix())
	content := fmt.Sprintf("version: %s\ndeploy_time: %s\n", version, time.Now().Format(time.RFC3339))

	tmpFile, err := os.CreateTemp("", "invoke-deploy-artifact-*.txt")
	if err != nil {
		return "", err
	}

	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()

		return "", err
	}

	if err := tmpFile.Close(); err != nil {
		return "", err
	}

	fmt.Println(infoStyle.Render("Created local artifact with version " + version))

	return tmpFile.Name(), nil
}

func uploadApp(ctx context.Context, env invoke.Environment, localPath string) error {
	mkdirCmd := &invoke.Command{Cmd: "mkdir", Args: []string{"-p", remoteAppDir}}
	if _, err := env.Run(ctx, mkdirCmd); err != nil {
		return fmt.Errorf("failed to create remote dir: %w", err)
	}

	remotePath := filepath.Join(remoteAppDir, remoteFile)

	err := env.Upload(ctx, localPath, remotePath, invoke.WithPermissions(0o644))
	if err != nil {
		return err
	}

	fmt.Println(checkStyle.Render("Upload complete."))

	return nil
}

func verifyDeployment(ctx context.Context, env invoke.Environment) error {
	remotePath := filepath.Join(remoteAppDir, remoteFile)
	cmd := &invoke.Command{Cmd: "cat", Args: []string{remotePath}}

	res, err := invoke.NewExecutor(env).RunBuffered(ctx, cmd)
	if err != nil {
		return err
	}

	fmt.Println(infoStyle.Render("Verified Deployed Version:\n" + string(res.Stdout)))

	return nil
}

func downloadAndVerify(ctx context.Context, env invoke.Environment, originalArtifactPath string) error {
	remotePath := filepath.Join(remoteAppDir, remoteFile)

	downloadedFile, err := os.CreateTemp("", "invoke-download-verify-*.txt")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(downloadedFile.Name()) }()

	_ = downloadedFile.Close() // Invoke writes to the path

	if err := env.Download(ctx, remotePath, downloadedFile.Name()); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	originalContent, err := os.ReadFile(originalArtifactPath)
	if err != nil {
		return err
	}

	downloadedContent, err := os.ReadFile(downloadedFile.Name())
	if err != nil {
		return err
	}

	if !bytes.Equal(originalContent, downloadedContent) {
		return fmt.Errorf("content mismatch! Original: %q, Downloaded: %q", originalContent, downloadedContent)
	}

	fmt.Println(checkStyle.Render("Downloaded file matches original artifact exactly."))

	return nil
}
