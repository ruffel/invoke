package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/providers/docker"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/ssh"
	"github.com/spf13/cobra"
)

var (
	// CLI Flags.
	ephemeral bool
	sshHost   string
	sshUser   string
	sshPort   int
	sshKey    string
	sshPass   string
	container string
)

const (
	targetLocal  = "local"
	targetSSH    = "ssh"
	targetDocker = "docker"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "unified-deploy",
		Short: "A demo CLI for the invoke library",
		Long:  `Demonstrates "Write once, run anywhere" deployment using invoke.`,
	}

	// Local Command
	localCmd := &cobra.Command{
		Use:   "local",
		Short: "Deploy to the local machine",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDeploy(targetLocal)
		},
	}

	// SSH Command
	sshCmd := &cobra.Command{
		Use:   "ssh",
		Short: "Deploy to a remote server via SSH",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDeploy(targetSSH)
		},
	}
	sshCmd.Flags().StringVar(&sshHost, "host", "", "SSH Hostname")
	sshCmd.Flags().StringVar(&sshUser, "user", "root", "SSH User")
	sshCmd.Flags().IntVar(&sshPort, "port", 22, "SSH Port")
	sshCmd.Flags().StringVar(&sshKey, "key", "", "Private key path")
	sshCmd.Flags().StringVar(&sshPass, "password", "", "Password (for testing)")
	sshCmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "Spin up an ephemeral SSH container")

	// Docker Command
	dockerCmd := &cobra.Command{
		Use:   "docker",
		Short: "Deploy to a running Docker container",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDeploy(targetDocker)
		},
	}
	dockerCmd.Flags().StringVar(&container, "container", "", "Container ID/Name")
	dockerCmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "Spin up an ephemeral target container")

	rootCmd.AddCommand(localCmd, sshCmd, dockerCmd)

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func runDeploy(target string) error {
	ctx := context.Background()

	// Title
	fmt.Println(titleStyle.Render("🚀 Starting Deployment to " + strings.ToUpper(target)))

	// Ephemeral Handling
	if ephemeral {
		fmt.Println(infoStyle.Render("✨ Provisioning ephemeral environment..."))

		config, cleanup, err := provisionEphemeral(ctx, target)
		if err != nil {
			return fmt.Errorf("provisioning failed: %w", err)
		}
		defer cleanup()

		// Apply config overrides to global flags
		switch target {
		case targetSSH:
			c, ok := config.(ssh.Config)
			if !ok {
				return fmt.Errorf("ephemeral provisioner returned unexpected config type for ssh: %T", config)
			}

			sshHost = c.Host
			sshPort = c.Port
			sshUser = c.User
			sshPass = c.Password
		case targetDocker:
			s, ok := config.(string)
			if !ok {
				return fmt.Errorf("ephemeral provisioner returned unexpected config type for docker: %T", config)
			}

			container = s
		}
	}

	// Resolve Docker Host if needed (don't fail hard, defaults might work)
	// Resolve Docker Host if needed (don't fail hard, defaults might work)
	if target == targetDocker {
		_ = resolveDockerHost(ctx)
	}

	// Init Environment
	env, err := initEnv(target)
	if err != nil {
		return err
	}

	defer func() { _ = env.Close() }()

	// Run Logic
	start := time.Now()

	if err := Deploy(ctx, env); err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("❌ DEPLOYMENT FAILED: %v", err)))

		return err
	}

	duration := time.Since(start)
	fmt.Println(checkStyle.Render(fmt.Sprintf("✅ DEPLOYMENT SUCCESSFUL (took %v)", duration)))

	return nil
}

func initEnv(target string) (invoke.Environment, error) {
	switch target {
	case targetLocal:
		return local.New()
	case targetSSH:
		if sshHost == "" {
			return nil, errors.New("missing --host")
		}

		cfg := ssh.Config{
			Host:               sshHost,
			Port:               sshPort,
			User:               sshUser,
			PrivateKeyPath:     sshKey,
			Password:           sshPass,
			Timeout:            10 * time.Second,
			InsecureSkipVerify: true,
		}

		return ssh.New(ssh.WithConfig(cfg))
	case targetDocker:
		if container == "" {
			return nil, errors.New("missing --container")
		}

		return docker.New(docker.WithContainerID(container))
	}

	return nil, errors.New("unknown target")
}
