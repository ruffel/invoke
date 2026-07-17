package invoke_test

import (
	"context"
	"fmt"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/fake"
)

// The quickstart: from a target to captured output in a handful of lines.
// The fake stands in for a real provider (local, ssh, docker) so the
// example is runnable; swap the constructor to change targets.
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
