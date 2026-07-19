package invoke_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/fake"
	"github.com/ruffel/invoke/local"
)

// The quickstart, against a real target: constructing the environment is
// the only line that differs between local, ssh and docker.
func ExampleNewExecutor() {
	env, err := local.New()
	if err != nil {
		panic(err)
	}

	defer func() { _ = env.Close() }()

	_, stdout, _, err := invoke.NewExecutor(env).Output(
		context.Background(), invoke.New("echo", "from the host"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s", stdout)
	// Output: from the host
}

// The fake stands in for a real provider wherever an example needs a
// predictable target; it obeys the same contracts.
func ExampleExecutor_Output() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env)

	_, stdout, _, err := exec.Output(context.Background(), invoke.New("echo", "hello"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s", stdout)
	// Output: hello
}

// Retry only re-runs transport failures, with a bounded backoff.
func ExampleWithRetry() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env, invoke.WithRetry(3, invoke.ConstantBackoff(0)))

	res, _, _, err := exec.Output(context.Background(), invoke.Shell("exit 0"))
	fmt.Println(res.ExitCode, err)
	// Output: 0 <nil>
}

// A command that runs and fails is a different thing from one that never
// ran, and the error says which. Branch on the kind, not on the message:
// messages name the transport and differ between targets.
func ExampleExitError() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env)

	_, err := exec.Run(context.Background(), invoke.Shell("exit 3"), invoke.IO{})

	var exitErr *invoke.ExitError

	switch {
	case errors.As(err, &exitErr):
		fmt.Println("ran and failed with", exitErr.Code)
	case errors.Is(err, invoke.ErrNotFound):
		fmt.Println("could not be started")
	case err != nil:
		fmt.Println("something else went wrong")
	}
	// Output: ran and failed with 3
}

// A missing executable is reported before anything runs, so it is never
// confused with a command that exited non-zero.
func ExampleErrNotFound() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	_, err := env.Start(context.Background(), invoke.New("no-such-tool"), invoke.IO{})

	fmt.Println(errors.Is(err, invoke.ErrNotFound))
	// Output: true
}

// Streams belong to the invocation rather than the command, so the same
// Command value can be run more than once without being rebuilt.
func ExampleIO() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	cmd := invoke.New("echo", "twice")

	for range 2 {
		var out strings.Builder

		proc, err := env.Start(context.Background(), cmd, invoke.IO{Stdout: &out})
		if err != nil {
			panic(err)
		}

		if _, err := proc.Wait(); err != nil {
			panic(err)
		}

		fmt.Print(out.String())
	}
	// Output:
	// twice
	// twice
}

// Ask the target what it can do rather than assuming: an undeclared
// capability fails rather than being silently ignored.
func ExampleCapabilities() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	if !env.Capabilities().TTY {
		_, err := env.Start(context.Background(), invoke.New("true"), invoke.IO{TTY: &invoke.TTY{}})
		fmt.Println("tty refused:", errors.Is(err, invoke.ErrNotSupported))
	}
	// Output: tty refused: true
}

// Transfers move a file or a whole tree, and are delivered whole or not
// at all.
func ExampleExecutor_Upload() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	source := filepath.Join(os.TempDir(), "invoke-example.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		panic(err)
	}

	defer func() { _ = os.Remove(source) }()

	exec := invoke.NewExecutor(env)
	if err := exec.Upload(context.Background(), source, "/tmp/delivered.txt"); err != nil {
		panic(err)
	}

	content, err := env.FS().Open("tmp/delivered.txt")
	if err != nil {
		panic(err)
	}

	defer func() { _ = content.Close() }()

	fmt.Println("delivered")
	// Output: delivered
}

// A test can teach the fake about a command its own code runs, and then
// assert on what was asked for. The fake refuses commands it knows
// nothing about rather than inventing a result, so an unregistered tool
// fails the same way a missing one would on a real target.
func ExampleEnvironment_Handle() {
	env := fake.New()

	defer func() { _ = env.Close() }()

	env.Handle("systemctl", func(_ context.Context, cmd invoke.Command, s *fake.Session) int {
		_, _ = fmt.Fprintf(s.Stdout, "asked to %s\n", strings.Join(cmd.Args, " "))

		return 0
	})

	exec := invoke.NewExecutor(env)

	_, stdout, _, err := exec.Output(context.Background(), invoke.New("systemctl", "restart", "nginx"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s", stdout)

	for _, call := range env.Calls() {
		fmt.Println("recorded:", call.Path, call.Args)
	}
	// Output:
	// asked to restart nginx
	// recorded: systemctl [restart nginx]
}
