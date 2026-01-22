// compat-check runs the invoke contract test suite against available providers.
//
// By default it runs against the local provider only. Use --all to also test
// against ephemeral Docker and SSH containers (requires Docker).
//
// Usage:
//
//	go run ./examples/compat-check
//	go run ./examples/compat-check --all
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/providers/docker"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/ssh"
)

func main() {
	runAll := len(os.Args) > 1 && os.Args[1] == "--all"

	ctx := context.Background()

	fmt.Println("invoke compatibility check")
	fmt.Println()

	envs, cleanups, skipped := setupEnvironments(ctx, runAll)
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	if len(envs) == 0 {
		fmt.Println("FAIL: no providers available")
		os.Exit(1)
	}

	names := make([]string, 0, len(envs))
	for n := range envs {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("providers: %s\n", strings.Join(names, ", "))
	for _, s := range skipped {
		fmt.Printf("  (skipped) %s\n", s)
	}
	fmt.Println()

	contracts := invoketest.AllContracts()

	// Collect all results into a matrix.
	type result struct {
		passed  bool
		skipped bool
		detail  string
	}

	matrix := make(map[string]map[string]result) // contract ID → provider → result
	for _, tc := range contracts {
		matrix[tc.ID()] = make(map[string]result)
		for name, env := range envs {
			r := runContract(ctx, tc, env)
			matrix[tc.ID()][name] = result{
				passed:  r.passed,
				skipped: r.skipped,
				detail:  r.errMsg + r.skipMsg,
			}
		}
	}

	// Compute column widths.
	const colW = 8
	nameW := len("CONTRACT")
	for _, tc := range contracts {
		if len(tc.ID()) > nameW {
			nameW = len(tc.ID())
		}
	}

	// Header.
	header := fmt.Sprintf("%-*s", nameW, "CONTRACT")
	for _, n := range names {
		header += fmt.Sprintf("  %-*s", colW, strings.ToUpper(n))
	}
	header += "  PARITY"
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Rows.
	var failures int
	var currentCat string

	for _, tc := range contracts {
		if cat := strings.SplitN(tc.ID(), "/", 2)[0]; cat != currentCat {
			currentCat = cat
			fmt.Println()
		}

		row := fmt.Sprintf("%-*s", nameW, tc.ID())
		anyFail, anySkip := false, false

		for _, n := range names {
			r := matrix[tc.ID()][n]
			var cell string
			switch {
			case r.skipped:
				cell = "SKIP"
				anySkip = true
			case r.passed:
				cell = "PASS"
			default:
				cell = "FAIL"
				anyFail = true
				failures++
			}
			row += fmt.Sprintf("  %-*s", colW, cell)
		}

		switch {
		case anyFail:
			row += "  FAIL"
		case anySkip:
			row += "  --"
		default:
			row += "  OK"
		}

		fmt.Println(row)

		// Print failure details below the row.
		if anyFail {
			for _, n := range names {
				if r := matrix[tc.ID()][n]; !r.passed && !r.skipped && r.detail != "" {
					fmt.Printf("  [%s] %s\n", n, r.detail)
				}
			}
		}
	}

	fmt.Println()

	if failures > 0 {
		fmt.Printf("FAIL: %d contract(s) failed\n", failures)

		os.Exit(1)
	}

	fmt.Println("ok: all contracts passed")
}

// --- Environment setup ---

func setupEnvironments(ctx context.Context, runAll bool) (map[string]invoke.Environment, []func(), []string) {
	envs := make(map[string]invoke.Environment)
	var cleanups []func()
	var skipped []string

	l, err := local.New()
	if err != nil {
		skipped = append(skipped, fmt.Sprintf("local: unavailable (%v)", err))
	} else {
		envs["local"] = l
		cleanups = append(cleanups, func() { _ = l.Close() })
	}

	if runAll {
		resolveDockerHost(ctx)

		if d, cleanup, err := provisionDocker(ctx); err != nil {
			skipped = append(skipped, fmt.Sprintf("docker: skipped (%v)", err))
		} else {
			envs["docker"] = d
			cleanups = append(cleanups, func() { cleanup(); _ = d.Close() })
		}

		if s, cleanup, err := provisionSSH(ctx); err != nil {
			skipped = append(skipped, fmt.Sprintf("ssh: skipped (%v)", err))
		} else {
			envs["ssh"] = s
			cleanups = append(cleanups, func() { cleanup(); _ = s.Close() })
		}
	}

	return envs, cleanups, skipped
}

// --- Contract runner ---

type contractResult struct {
	passed  bool
	skipped bool
	errMsg  string
	skipMsg string
}

type tester struct {
	ctx      context.Context //nolint:containedctx
	name     string
	failed   bool
	skipped  bool
	errMsg   string
	skipMsg  string
	tempDirs []string
}

func (t *tester) Errorf(f string, a ...any)  { t.failed = true; t.errMsg = fmt.Sprintf(f, a...) }
func (t *tester) FailNow()                   { t.failed = true; panic(failNow{}) }
func (t *tester) Skipf(f string, a ...any)   { t.skipped = true; t.skipMsg = fmt.Sprintf(f, a...); panic(skipNow{}) }
func (t *tester) Context() context.Context   { return t.ctx }
func (t *tester) Name() string               { return t.name }
func (t *tester) TempDir() string {
	dir, err := os.MkdirTemp("", "invoke-compat-*")
	if err != nil {
		panic(err)
	}

	t.tempDirs = append(t.tempDirs, dir)

	return dir
}

func (t *tester) cleanup() {
	for _, d := range t.tempDirs {
		_ = os.RemoveAll(d)
	}
}

type failNow struct{}
type skipNow struct{}

func runContract(ctx context.Context, tc invoketest.TestCase, env invoke.Environment) contractResult {
	t := &tester{ctx: ctx, name: tc.ID()}
	defer t.cleanup()

	func() {
		defer func() {
			if r := recover(); r != nil {
				switch r.(type) {
				case failNow, skipNow:
				default:
					panic(r)
				}
			}
		}()

		if tc.Prereq != nil {
			ok, reason := tc.Prereq(t, env)
			if !ok {
				t.Skipf("prereq: %s", reason)
			}
		}

		tc.Run(t, env)
	}()

	return contractResult{
		passed:  !t.failed && !t.skipped,
		skipped: t.skipped,
		errMsg:  t.errMsg,
		skipMsg: t.skipMsg,
	}
}

// --- Ephemeral provisioning ---

const (
	sshWaitTimeout  = 30 * time.Second
	sshPollInterval = 500 * time.Millisecond
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

func provisionDocker(ctx context.Context) (invoke.Environment, func(), error) {
	l, err := local.New()
	if err != nil {
		return nil, nil, err
	}

	exec := invoke.NewExecutor(l)

	res, err := exec.RunBuffered(ctx, &invoke.Command{
		Cmd:  "docker",
		Args: []string{"run", "-d", "--rm", "alpine", "sleep", "infinity"},
	})
	if err != nil {
		_ = l.Close()

		return nil, nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))
	cleanup := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:contextcheck
		defer cancel()

		_, _ = exec.Run(stopCtx, &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})
		_ = l.Close()
	}

	d, err := docker.New(docker.WithContainerID(cid))
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	return d, cleanup, nil
}

func provisionSSH(ctx context.Context) (invoke.Environment, func(), error) {
	l, err := local.New()
	if err != nil {
		return nil, nil, err
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

		return nil, nil, err
	}

	cid := strings.TrimSpace(string(res.Stdout))
	cleanup := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:contextcheck
		defer cancel()

		_, _ = exec.Run(stopCtx, &invoke.Command{Cmd: "docker", Args: []string{"stop", cid}})
		_ = l.Close()
	}

	host, port, err := resolveSSHPort(ctx, exec, cid)
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	if err := waitForSSH(ctx, host, port); err != nil {
		cleanup()

		return nil, nil, err
	}

	s, err := ssh.New(ssh.WithConfig(ssh.Config{
		Host:               host,
		Port:               port,
		User:               "testuser",
		Password:           "password",
		InsecureSkipVerify: true,
	}))
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	return s, cleanup, nil
}

func resolveSSHPort(ctx context.Context, exec *invoke.Executor, cid string) (string, int, error) {
	for range 10 {
		res, err := exec.RunBuffered(ctx, &invoke.Command{
			Cmd:  "docker",
			Args: []string{"port", cid, "2222/tcp"},
		})
		if err != nil {
			time.Sleep(sshPollInterval)

			continue
		}

		line := strings.TrimSpace(strings.Split(string(res.Stdout), "\n")[0])
		if line == "" {
			time.Sleep(sshPollInterval)

			continue
		}

		h, portStr, err := net.SplitHostPort(line)
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

	return "", 0, errors.New("failed to resolve SSH port")
}

func waitForSSH(ctx context.Context, host string, port int) error {
	deadline := time.Now().Add(sshWaitTimeout)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			time.Sleep(time.Second) // let sshd finish startup

			return nil
		}

		time.Sleep(sshPollInterval)
	}

	return errors.New("timeout waiting for SSH")
}
