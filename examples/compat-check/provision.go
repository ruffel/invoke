package main

import (
	"context"
	"errors"
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
	sshWaitTimeout  = 30 * time.Second
	sshPollInterval = 500 * time.Millisecond
	sshInternalPort = "2222/tcp"
	cleanupTimeout  = 5 * time.Second
)

func resolveDockerHost(ctx context.Context) {
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}

	l, err := local.New()
	if err != nil {
		return
	}

	defer func() { _ = l.Close() }()

	exec := invoke.NewExecutor(l)

	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"},
	})
	if err != nil {
		return
	}

	if host := strings.TrimSpace(string(res.Stdout)); host != "" {
		_ = os.Setenv("DOCKER_HOST", host)
	}
}

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

func provisionEphemeralDocker(ctx context.Context) (string, func(), error) {
	l, err := local.New()
	if err != nil {
		return "", nil, err
	}

	exec := invoke.NewExecutor(l)

	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"run", "-d", "--rm", "alpine", "sleep", "infinity"},
	})
	if err != nil {
		_ = l.Close()

		return "", nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))
	cleanup := func() { //nolint:contextcheck
		ctxCleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		_, _ = exec.Run(ctxCleanup, &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})

		_ = l.Close()
	}

	return cid, cleanup, nil
}

func provisionEphemeralSSH(ctx context.Context) (ssh.Config, func(), error) {
	l, err := local.New()
	if err != nil {
		return ssh.Config{}, nil, err
	}

	exec := invoke.NewExecutor(l)

	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd: "docker",
		Args: []string{
			"run", "-d", "--rm", "-P",
			"-e", "USER_NAME=testuser",
			"-e", "PASSWORD_ACCESS=true",
			"-e", "USER_PASSWORD=password",
			"lscr.io/linuxserver/openssh-server:latest",
		},
	})
	if err != nil {
		_ = l.Close()

		return ssh.Config{}, nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))
	cleanup := func() { //nolint:contextcheck
		ctxCleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		_, _ = exec.Run(ctxCleanup, &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})

		_ = l.Close()
	}

	sshHost, sshPort, err := resolveSSHPort(ctx, exec, cid)
	if err != nil {
		cleanup()

		return ssh.Config{}, nil, err
	}

	candidates := []string{sshHost}
	if sshHost == "127.0.0.1" {
		if gw := getBridgeGateway(ctx, exec); gw != "" && gw != "127.0.0.1" {
			candidates = append(candidates, gw)
		}
	}

	finalHost, err := waitForSSHReady(ctx, candidates, sshPort)
	if err != nil {
		cleanup()

		return ssh.Config{}, nil, err
	}

	return ssh.Config{
		Host:     finalHost,
		Port:     sshPort,
		User:     "testuser",
		Password: "password",
	}, cleanup, nil
}

func resolveSSHPort(ctx context.Context, exec *invoke.Executor, cid string) (string, int, error) {
	for range 10 {
		pRes, pErr := exec.RunBuffered(ctx, &invoke.Command{Cmd: "docker", Args: []string{"port", cid, sshInternalPort}})
		if pErr != nil {
			time.Sleep(sshPollInterval)

			continue
		}

		output := strings.TrimSpace(string(pRes.Stdout))
		lines := strings.Split(output, "\n")

		if len(lines) == 0 || lines[0] == "" {
			time.Sleep(sshPollInterval)

			continue
		}

		h, portStr, err := net.SplitHostPort(lines[0])
		if err != nil {
			time.Sleep(sshPollInterval)

			continue
		}

		if h == "0.0.0.0" || h == "::" {
			h = "127.0.0.1"
		}

		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 {
			time.Sleep(sshPollInterval)

			continue
		}

		return h, p, nil
	}

	return "", 0, errors.New("failed to get port mapping")
}

func waitForSSHReady(ctx context.Context, candidates []string, port int) (string, error) {
	deadline := time.Now().Add(sshWaitTimeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		for _, host := range candidates {
			addr := net.JoinHostPort(host, strconv.Itoa(port))

			var dialer net.Dialer

			conn, err := dialer.DialContext(ctx, "tcp", addr)
			if err == nil {
				_ = conn.Close()

				time.Sleep(1 * time.Second)

				return host, nil
			}
		}

		time.Sleep(sshPollInterval)
	}

	return "", errors.New("timeout waiting for ssh")
}
