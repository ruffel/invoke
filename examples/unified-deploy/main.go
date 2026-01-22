// unified-deploy demonstrates invoke's "write once, run anywhere" pattern.
//
// The same deploy function runs identically against local, SSH, and Docker
// environments. Pass the target as the first argument:
//
//	go run ./examples/unified-deploy local
//	go run ./examples/unified-deploy ssh   --host 10.0.0.1 --user deploy --password secret
//	go run ./examples/unified-deploy docker --container my-app
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/providers/docker"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/ssh"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: unified-deploy <local|ssh|docker> [flags]")
		os.Exit(1)
	}

	env, err := initEnv(os.Args[1], os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		os.Exit(1)
	}

	defer func() { _ = env.Close() }()

	ctx := context.Background()

	start := time.Now()

	if err := deploy(ctx, env); err != nil {
		fmt.Fprintf(os.Stderr, "deploy failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\ndone (%v)\n", time.Since(start).Round(time.Millisecond))
}

// deploy runs the same pipeline regardless of environment.
func deploy(ctx context.Context, env invoke.Environment) error {
	exec := invoke.NewExecutor(env)

	// Pre-flight: identify the target system.
	fmt.Printf("%-14s", "pre-flight")

	res, err := exec.RunBuffered(ctx, &invoke.Command{Cmd: "uname", Args: []string{"-sm"}})
	if err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	}

	fmt.Printf("%s", strings.TrimSpace(string(res.Stdout))+"\n")

	// Generate and upload an artifact.
	artifact, err := os.CreateTemp("", "invoke-deploy-*.txt")
	if err != nil {
		return err
	}

	content := fmt.Sprintf("version: v1.0.0-%d\ndeployed: %s\n", time.Now().Unix(), time.Now().Format(time.RFC3339))

	if _, err := artifact.WriteString(content); err != nil {
		return err
	}

	_ = artifact.Close()

	defer func() { _ = os.Remove(artifact.Name()) }()

	remotePath := "/tmp/invoke-deploy/version.txt"

	if _, err := env.Run(ctx, &invoke.Command{Cmd: "mkdir", Args: []string{"-p", "/tmp/invoke-deploy"}}); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	fmt.Printf("%-14s", "upload")

	if err := env.Upload(ctx, artifact.Name(), remotePath, invoke.WithPermissions(0o644)); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	fmt.Println("ok")

	// Verify the file on the remote.
	fmt.Printf("%-14s", "verify")

	res, err = exec.RunBuffered(ctx, &invoke.Command{Cmd: "cat", Args: []string{remotePath}})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	fmt.Printf("%s", strings.TrimSpace(string(res.Stdout))+"\n")

	// Download and compare.
	downloaded := filepath.Join(os.TempDir(), "invoke-deploy-downloaded.txt")

	defer func() { _ = os.Remove(downloaded) }()

	fmt.Printf("%-14s", "round-trip")

	if err := env.Download(ctx, remotePath, downloaded); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	data, err := os.ReadFile(downloaded)
	if err != nil {
		return err
	}

	if string(data) != content {
		return fmt.Errorf("content mismatch: got %q, want %q", data, content)
	}

	fmt.Println("ok")

	return nil
}

// initEnv creates the appropriate environment from CLI args.
func initEnv(target string, flags []string) (invoke.Environment, error) {
	switch target {
	case "local":
		return local.New()

	case "ssh":
		host, user, password, port := "", "root", "", 22

		for i := 0; i < len(flags)-1; i += 2 {
			switch flags[i] {
			case "--host":
				host = flags[i+1]
			case "--user":
				user = flags[i+1]
			case "--password":
				password = flags[i+1]
			case "--port":
				fmt.Sscanf(flags[i+1], "%d", &port)
			}
		}

		if host == "" {
			return nil, fmt.Errorf("ssh requires --host")
		}

		return ssh.New(ssh.WithConfig(ssh.Config{
			Host:               host,
			Port:               port,
			User:               user,
			Password:           password,
			InsecureSkipVerify: true,
		}))

	case "docker":
		container := ""

		for i := 0; i < len(flags)-1; i += 2 {
			if flags[i] == "--container" {
				container = flags[i+1]
			}
		}

		if container == "" {
			return nil, fmt.Errorf("docker requires --container")
		}

		return docker.New(docker.WithContainerID(container))

	default:
		return nil, fmt.Errorf("unknown target %q (use local, ssh, or docker)", target)
	}
}
