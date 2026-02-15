package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/ssh"
)

const (
	// sshWaitTimeout is the max time to wait for the ephemeral SSH container to accept connections.
	sshWaitTimeout = 30 * time.Second
	// sshPollInterval is the frequency of connection attempts.
	sshPollInterval = 500 * time.Millisecond
	// sshInternalPort is the port exposed by the container (openssh-server default).
	sshInternalPort = "2222/tcp"
)

// resolveDockerHost ensures a valid DOCKER_HOST is set.
// It inspects the current docker context if the environment variable is missing.
func resolveDockerHost(ctx context.Context) error {
	if os.Getenv("DOCKER_HOST") != "" {
		return nil
	}

	l, err := local.New()
	if err != nil {
		return err
	}

	defer func() { _ = l.Close() }()

	exec := invoke.NewExecutor(l)

	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"},
	})
	if err != nil {
		return nil
	}

	host := strings.TrimSpace(string(res.Stdout))
	if host != "" {
		return os.Setenv("DOCKER_HOST", host)
	}

	return nil
}

// getBridgeGateway finds the Docker bridge gateway IP.
// Required for connecting to container ports from within a Docker-in-Docker environment.
func getBridgeGateway(ctx context.Context, exec *invoke.Executor) string {
	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"},
	})
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(res.Stdout))
}

// provisionEphemeral spins up a target environment for the demo.
func provisionEphemeral(ctx context.Context, target string) (any, func(), error) {
	localEnv, err := local.New()
	if err != nil {
		return nil, nil, err
	}

	exec := invoke.NewExecutor(localEnv)

	var (
		config  any
		cleanup func()
	)

	switch target {
	case "docker":
		config, cleanup, err = provisionEphemeralDocker(ctx, exec)
	case "ssh":
		config, cleanup, err = provisionEphemeralSSH(ctx, exec)
	default:
		_ = localEnv.Close()

		return nil, nil, fmt.Errorf("ephemeral mode not supported for target: %s", target)
	}

	if err != nil {
		_ = localEnv.Close()

		return nil, nil, err
	}

	return config, func() {
		if cleanup != nil {
			cleanup()
		}

		_ = localEnv.Close()
	}, nil
}

func provisionEphemeralDocker(ctx context.Context, exec *invoke.Executor) (string, func(), error) {
	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"run", "-d", "--rm", "alpine", "sleep", "infinity"},
	})
	if err != nil {
		return "", nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))

	cleanup := func() { //nolint:contextcheck // Cleanup must run independently of cancelled parent context
		fmt.Println(infoStyle.Render("🧹 Cleaning up..."))

		_, _ = exec.Run(context.Background(), &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})
	}

	return cid, cleanup, nil
}

func provisionEphemeralSSH(ctx context.Context, exec *invoke.Executor) (ssh.Config, func(), error) {
	// Launch linuxserver/openssh-server
	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd: "docker",
		Args: []string{
			"run", "-d", "--rm", "-P",
			"-e", "PUID=1000", "-e", "PGID=1000",
			"-e", "USER_NAME=testuser", "-e", "SUDO_ACCESS=true",
			"-e", "PASSWORD_ACCESS=true", "-e", "USER_PASSWORD=password",
			"lscr.io/linuxserver/openssh-server:latest",
		},
	})
	if err != nil {
		return ssh.Config{}, nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))

	cleanup := func() { //nolint:contextcheck // Cleanup must run independently of cancelled parent context
		fmt.Println(infoStyle.Render("🧹 Cleaning up ephemeral SSH container..."))

		_, _ = exec.Run(context.Background(), &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})
	}

	// Resolve the dynamic port mapping
	sshHost, sshPort, err := findContainerSSHPort(ctx, exec, cid)
	if err != nil {
		cleanup()

		return ssh.Config{}, nil, err
	}

	fmt.Println(infoStyle.Render(fmt.Sprintf("   Container %s running on port %d. Waiting for sshd...", cid[:12], sshPort)))

	// Build connection candidates
	candidates := []string{sshHost}

	// If the host is local, we might be inside a container (DinD).
	// Attempt to reach the host via the bridge gateway if direct localhost fails.
	if sshHost == "127.0.0.1" {
		if gw := getBridgeGateway(ctx, exec); gw != "" && gw != "127.0.0.1" {
			candidates = append(candidates, gw)
		}
	}

	finalHost, err := waitForSSHReady(ctx, candidates, sshPort)
	if err != nil {
		cleanup()

		return ssh.Config{}, nil, fmt.Errorf("ssh service never became ready: %w", err)
	}

	return ssh.Config{
		Host:     finalHost,
		Port:     sshPort,
		User:     "testuser",
		Password: "password",
	}, cleanup, nil
}

func findContainerSSHPort(ctx context.Context, exec *invoke.Executor, cid string) (string, int, error) {
	for range 10 {
		pRes, pErr := exec.RunBuffered(ctx, &invoke.Command{Cmd: "docker", Args: []string{"port", cid, sshInternalPort}})
		if pErr == nil {
			output := strings.TrimSpace(string(pRes.Stdout))
			for line := range strings.SplitSeq(output, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				h, portStr, err := net.SplitHostPort(line)
				if err != nil {
					continue
				}

				// Normalize wildcard bindings
				if h == "0.0.0.0" || h == "::" {
					h = "127.0.0.1"
				}

				if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
					return h, p, nil
				}
			}
		}

		time.Sleep(sshPollInterval)
	}

	return "", 0, fmt.Errorf("failed to get port mapping for container %s", cid)
}

func waitForSSHReady(ctx context.Context, candidates []string, port int) (string, error) {
	deadline := time.Now().Add(sshWaitTimeout)

	for time.Now().Before(deadline) {
		// Check for parent context cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		for _, host := range candidates {
			addr := net.JoinHostPort(host, strconv.Itoa(port))

			var d net.Dialer

			conn, err := d.DialContext(ctx, "tcp", addr)
			if err == nil {
				_ = conn.Close()
				// Allow a brief settling time for the SSH handshake protocol to be ready
				time.Sleep(1 * time.Second)

				return host, nil
			}
		}

		time.Sleep(sshPollInterval)
	}

	return "", fmt.Errorf("timeout waiting for %v on port %d", candidates, port)
}
