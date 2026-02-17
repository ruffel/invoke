// Package main provides a parity checker for the invoke library.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/invoketest"
	"github.com/ruffel/invoke/providers/docker"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/ssh"
	"github.com/spf13/cobra"
)

func main() {
	var runAll bool

	rootCmd := &cobra.Command{
		Use:   "compat-check",
		Short: "Certified provider parity checker for invoke",
		Long:  `Runs the official invoke compatibility suite across different providers to ensure parity.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := context.Background()

			fmt.Println(titleStyle.Render("🔍 Invoke Compatibility Check"))

			envs, cleanups := setupEnvironments(ctx, runAll)

			defer func() {
				for _, c := range cleanups {
					c()
				}
			}()

			if len(envs) == 0 {
				return errors.New("no providers available to test")
			}

			fmt.Println(infoStyle.Render("🚀 Running Compatibility Suite against: " + strings.Join(getSortedEnvNames(envs), ", ")))

			matrix := runMatrix(ctx, envs)
			renderMatrix(envs, matrix)

			return nil
		},
	}
	rootCmd.Flags().BoolVar(&runAll, "all", false, "Run against all available providers (requires Docker)")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func setupEnvironments(ctx context.Context, runAll bool) (map[string]invoke.Environment, []func()) {
	envs := make(map[string]invoke.Environment)

	var cleanups []func()

	l, err := local.New()
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("❌ Local provider failed: %v", err)))
	} else {
		envs["local"] = l

		cleanups = append(cleanups, func() { _ = l.Close() })
	}

	if runAll {
		setupAllProviders(ctx, envs, &cleanups)
	}

	return envs, cleanups
}

func setupAllProviders(ctx context.Context, envs map[string]invoke.Environment, cleanups *[]func()) {
	resolveDockerHost(ctx)

	if d, cleanup, err := setupDocker(ctx); err == nil {
		envs["docker"] = d

		*cleanups = append(*cleanups, func() {
			if cleanup != nil {
				cleanup()
			}

			_ = d.Close()
		})
	}

	if s, cleanup, err := setupSSH(ctx); err == nil {
		envs["ssh"] = s

		*cleanups = append(*cleanups, func() {
			if cleanup != nil {
				cleanup()
			}

			_ = s.Close()
		})
	}
}

func setupDocker(ctx context.Context) (invoke.Environment, func(), error) {
	fmt.Println(infoStyle.Render("🐳 Provisioning ephemeral Docker container..."))

	cid, dCleanup, err := provisionEphemeralDocker(ctx)
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("⚠️  Docker provision failed: %v", err)))

		return nil, nil, err
	}

	d, err := docker.New(docker.WithContainerID(cid))
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("⚠️  Docker init failed: %v", err)))

		dCleanup()

		return nil, nil, err
	}

	return d, dCleanup, nil
}

func setupSSH(ctx context.Context) (invoke.Environment, func(), error) {
	fmt.Println(infoStyle.Render("🔑 Provisioning ephemeral SSH container..."))

	cfg, sCleanup, err := provisionEphemeralSSH(ctx)
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("⚠️  SSH provision failed: %v", err)))

		return nil, nil, err
	}

	s, err := ssh.New(ssh.WithConfig(ssh.Config{ //nolint:contextcheck
		Host:               cfg.Host,
		Port:               cfg.Port,
		User:               cfg.User,
		Password:           cfg.Password,
		InsecureSkipVerify: true,
	}))
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("⚠️  SSH init failed: %v", err)))

		sCleanup()

		return nil, nil, err
	}

	return s, sCleanup, nil
}

type testResult struct {
	passed  bool
	skipped bool
	errMsg  string
	skipMsg string
}

type cliTester struct {
	ctx      context.Context //nolint:containedctx
	name     string
	failed   bool
	skipped  bool
	errMsg   string
	skipMsg  string
	tempDirs []string
}

func (c *cliTester) Errorf(f string, a ...any) {
	c.failed = true
	c.errMsg = fmt.Sprintf(f, a...)
}

func (c *cliTester) FailNow() {
	c.failed = true

	panic(failNow{})
}

func (c *cliTester) Skipf(f string, a ...any) {
	c.skipped = true
	c.skipMsg = fmt.Sprintf(f, a...)

	panic(skipNow{})
}

func (c *cliTester) Context() context.Context {
	return c.ctx
}

func (c *cliTester) Name() string {
	return c.name
}

func (c *cliTester) TempDir() string {
	dir, err := os.MkdirTemp("", "invoke-compat-*")
	if err != nil {
		panic(err)
	}

	c.tempDirs = append(c.tempDirs, dir)

	return dir
}

func (c *cliTester) Cleanup() {
	for _, dir := range c.tempDirs {
		_ = os.RemoveAll(dir)
	}
}

type failNow struct{}

type skipNow struct{}

func runMatrix(ctx context.Context, envs map[string]invoke.Environment) map[string]map[string]testResult {
	data := make(map[string]map[string]testResult)

	for name, env := range envs {
		for _, tc := range invoketest.AllContracts() {
			func(tc invoketest.TestCase) {
				t := &cliTester{
					ctx:  ctx,
					name: tc.ID(),
				}
				defer t.Cleanup()

				func() {
					defer func() {
						if r := recover(); r != nil {
							if _, ok := r.(failNow); ok {
								return
							}

							if _, ok := r.(skipNow); ok {
								return
							}

							panic(r)
						}
					}()

					if tc.Prereq != nil {
						ok, reason := tc.Prereq(t, env)
						if !ok {
							t.Skipf("prereq unmet: %s", reason)
						}
					}

					tc.Run(t, env)
				}()

				id := tc.ID()
				if _, ok := data[id]; !ok {
					data[id] = make(map[string]testResult)
				}

				data[id][name] = testResult{
					passed:  !t.failed && !t.skipped,
					skipped: t.skipped,
					errMsg:  t.errMsg,
					skipMsg: t.skipMsg,
				}
			}(tc)
		}
	}

	return data
}

func getSortedEnvNames(envs map[string]invoke.Environment) []string {
	names := make([]string, 0, len(envs))
	for n := range envs {
		names = append(names, n)
	}

	sort.Strings(names)

	return names
}

func renderMatrix(envs map[string]invoke.Environment, matrix map[string]map[string]testResult) {
	names := getSortedEnvNames(envs)
	nameWidth := 30
	colWidth := 15

	for _, n := range names {
		if len(n)+2 > colWidth {
			colWidth = len(n) + 2
		}
	}

	renderHeader(names, nameWidth, colWidth)

	var (
		currentCat string
		issues     []string
		hasNA      bool
	)

	for _, tc := range invoketest.AllContracts() {
		if tc.Category != currentCat {
			currentCat = tc.Category
			fmt.Printf("%s\n", catStyle.Render(strings.ToUpper(currentCat)))
		}

		row := matrix[tc.ID()]
		issue, rowHasNA := renderRow(tc, names, row, nameWidth, colWidth)
		if rowHasNA {
			hasNA = true
		}

		if issue != "" {
			issues = append(issues, issue)
		}
	}

	if len(issues) > 0 {
		fmt.Println(errorStyle.Render("\n❌ Issue Details:"))

		for _, issue := range issues {
			fmt.Printf("  - %s\n", issue)
		}
	} else if hasNA {
		fmt.Println(infoStyle.Render("\n⚠️  No failures detected; some contracts are skipped (parity N/A)."))
	} else {
		fmt.Println(checkStyle.Render("\n✅ All providers are in parity!"))
	}

	fmt.Println()
}

func renderHeader(names []string, nameWidth, colWidth int) {
	var header strings.Builder

	header.WriteString(headerStyle.Render(fmt.Sprintf("%-*s", nameWidth, "CONTRACT TEST")))

	for _, n := range names {
		header.WriteString(" ")
		header.WriteString(headerStyle.Render(fmt.Sprintf("%-*s", colWidth, strings.ToUpper(n))))
	}

	header.WriteString(" ")
	header.WriteString(headerStyle.Render(fmt.Sprintf("%-*s", 10, "PARITY")))

	fmt.Println("\n" + header.String())
}

func renderRow(tc invoketest.TestCase, names []string, row map[string]testResult, nameWidth, colWidth int) (string, bool) {
	var line strings.Builder

	line.WriteString(rowStyle.Render(fmt.Sprintf("%-*s", nameWidth, tc.Name)))

	anySkipped := false

	var issue string

	for _, n := range names {
		res := row[n]
		status := "PASSED"
		style := passedStyle

		if res.skipped {
			status = "SKIPPED"
			style = skippedStyle
			anySkipped = true
		} else if !res.passed {
			status = "FAILED"
			style = failedStyle
			issue = fmt.Sprintf("[%s] %s/%s: %s", strings.ToUpper(n), tc.Category, tc.Name, res.errMsg)
		}

		line.WriteString(" ")
		line.WriteString(style.Render(fmt.Sprintf("%-*s", colWidth, status)))
	}

	parity := parityMatchStyle.Render("MATCH")
	if issue != "" {
		parity = parityDivergedStyle.Render("DIVERGED")
	} else if anySkipped {
		parity = parityNAStyle.Render("N/A")
	}

	line.WriteString(" ")
	line.WriteString(parity)

	fmt.Println(line.String())

	return issue, anySkipped
}
