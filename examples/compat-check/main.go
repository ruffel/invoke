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
	}

	if runAll {
		resolveDockerHost(ctx)
		if d, cleanup, err := setupDocker(ctx); err == nil {
			envs["docker"] = d
			cleanups = append(cleanups, cleanup)
		}

		if s, cleanup, err := setupSSH(ctx); err == nil {
			envs["ssh"] = s
			cleanups = append(cleanups, cleanup)
		}
	}

	return envs, cleanups
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
	passed bool
	errMsg string
}

type cliTester struct {
	ctx    context.Context //nolint:containedctx
	failed bool
	errMsg string
}

func (c *cliTester) Errorf(f string, a ...any) {
	c.failed = true
	c.errMsg = fmt.Sprintf(f, a...)
}

func (c *cliTester) FailNow() {
	c.failed = true
	panic(failNow{})
}

func (c *cliTester) Context() context.Context {
	return c.ctx
}

type failNow struct{}

func runMatrix(ctx context.Context, envs map[string]invoke.Environment) map[string]map[string]testResult {
	data := make(map[string]map[string]testResult)

	for name, env := range envs {
		for _, tc := range invoketest.AllContracts() {
			t := &cliTester{ctx: ctx}

			func() {
				defer func() {
					if r := recover(); r != nil {
						if _, ok := r.(failNow); ok {
							return
						}
						panic(r)
					}
				}()
				tc.Run(t, env)
			}()

			if _, ok := data[tc.Name]; !ok {
				data[tc.Name] = make(map[string]testResult)
			}
			data[tc.Name][name] = testResult{!t.failed, t.errMsg}
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

	header := headerStyle.Render(fmt.Sprintf("%-*s", nameWidth, "CONTRACT TEST"))
	for _, n := range names {
		header += " " + headerStyle.Render(fmt.Sprintf("%-*s", colWidth, strings.ToUpper(n)))
	}
	header += " " + headerStyle.Render(fmt.Sprintf("%-*s", 10, "PARITY"))
	fmt.Println("\n" + header)

	var (
		currentCat string
		issues     []string
	)

	for _, tc := range invoketest.AllContracts() {
		if tc.Category != currentCat {
			currentCat = tc.Category
			fmt.Printf("%s\n", catStyle.Render(strings.ToUpper(currentCat)))
		}

		row := matrix[tc.Name]
		line := rowStyle.Render(fmt.Sprintf("%-*s", nameWidth, tc.Name))
		allPassed := true

		for _, n := range names {
			res := row[n]
			status := "PASSED"
			style := passedStyle

			if !res.passed {
				status = "FAILED"
				style = failedStyle
				allPassed = false
				issues = append(issues, fmt.Sprintf("[%s] %s/%s: %s", strings.ToUpper(n), tc.Category, tc.Name, res.errMsg))
			}
			line += " " + style.Render(fmt.Sprintf("%-*s", colWidth, status))
		}

		parity := parityMatchStyle.Render("MATCH")
		if !allPassed {
			parity = parityDivergedStyle.Render("DIVERGED")
		}
		line += " " + parity
		fmt.Println(line)
	}

	if len(issues) > 0 {
		fmt.Println(errorStyle.Render("\n❌ Issue Details:"))
		for _, issue := range issues {
			fmt.Printf("  - %s\n", issue)
		}
	} else {
		fmt.Println(checkStyle.Render("\n✅ All providers are in parity!"))
	}
	fmt.Println()
}
