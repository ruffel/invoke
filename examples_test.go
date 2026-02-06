package invoke_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ruffel/invoke"
	"github.com/ruffel/invoke/providers/local"
	"github.com/ruffel/invoke/providers/mock"
	"github.com/ruffel/invoke/providers/ssh"
	testifymock "github.com/stretchr/testify/mock"
)

func ExampleExecutor_RunBuffered_local() {
	env, err := local.New()
	if err != nil {
		panic(err)
	}

	defer func() { _ = env.Close() }()

	exec := invoke.NewExecutor(env)

	cmd := invoke.Command{
		Cmd:  "echo",
		Args: []string{"hello", "world"},
	}

	ctx := context.Background()

	res, err := exec.RunBuffered(ctx, &cmd)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s\n", res.Stdout)
	// Output: hello world
}

func ExampleExecutor_Run_sudo() {
	// Example of using high-level options
	env := mock.New() // Using mock for safety in example

	// Mock the result
	// Match any command that has the right string components, ignoring pointers (Stdout/Stderr)
	matcher := testifymock.MatchedBy(func(c *invoke.Command) bool {
		return c.Cmd == "sudo" && len(c.Args) == 4 && c.Args[3] == "/root"
	})

	env.On("Run", context.Background(), matcher).Run(func(args testifymock.Arguments) {
		cmd := args.Get(1).(*invoke.Command)
		if cmd.Stdout != nil {
			_, _ = fmt.Fprint(cmd.Stdout, "secret.txt\n")
		}
	}).Return(&invoke.Result{ExitCode: 0}, nil)

	exec := invoke.NewExecutor(env)
	ctx := context.Background()

	// WithSudo() automatically wraps the command
	cmd := invoke.Command{Cmd: "ls", Args: []string{"/root"}}

	// We use RunBuffered to capture output
	res, err := exec.RunBuffered(ctx, &cmd, invoke.WithSudo())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Sudo Output: %s", res.Stdout)
	// Output: Sudo Output: secret.txt
}

func ExampleEnvironment_upload() {
	// Demonstrating the FileTransfer interface usage (now part of Environment)
	var env invoke.Environment = mock.New()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Setup mock expectation
	env.(*mock.Environment).On("Upload", ctx, "./config.json", "/etc/app/config.json", testifymock.Anything).Return(nil)
	// Upload with permissions override
	err := env.Upload(ctx, "./config.json", "/etc/app/config.json",
		invoke.WithPermissions(0o600),
	)
	if err != nil {
		log.Printf("Upload failed: %v", err)
	}
	// Output:
}

func Example_sshConfigReader() {
	// Example of loading SSH config from a string (or file)
	configContent := `
Host prod-db
  HostName 10.0.0.5
  User admin
  Port 2222
  IdentityFile ~/.ssh/prod_key.pem
  StrictHostKeyChecking no
`
	// Parse the config
	cfg, err := ssh.NewFromSSHConfigReader("prod-db", strings.NewReader(configContent))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Host: %s\n", cfg.Host)
	fmt.Printf("User: %s\n", cfg.User)
	fmt.Printf("Port: %d\n", cfg.Port)

	// Output:
	// Host: 10.0.0.5
	// User: admin
	// Port: 2222
}

func ExampleEnvironment_withProgress() {
	env, err := local.New()
	if err != nil {
		panic(err)
	}

	_ = os.WriteFile("largefile.dat", []byte("1234567890"), 0o600)

	defer func() { _ = os.Remove("largefile.dat") }()
	defer func() { _ = os.Remove("largefile.dat.bak") }()

	ctx := context.Background()

	err = env.Upload(ctx, "largefile.dat", "largefile.dat.bak",
		invoke.WithProgress(func(current, total int64) {
			fmt.Printf("Transferred %d/%d bytes\n", current, total)
		}),
	)
	if err != nil {
		panic(err)
	}

	// Output:
	// Transferred 10/10 bytes
}

func ExampleEnvironment_upload_download() {
	env, err := local.New()
	if err != nil {
		panic(err)
	}

	_ = os.WriteFile("localfile.txt", []byte("hello world"), 0o600)

	defer func() { _ = os.Remove("localfile.txt") }()
	defer func() { _ = os.Remove("localfile.bak") }()

	ctx := context.Background()

	err = env.Upload(ctx, "localfile.txt", "/tmp/localfile.txt", invoke.WithPermissions(0o644))
	if err != nil {
		panic(err)
	}

	err = env.Download(ctx, "/tmp/localfile.txt", "localfile.bak")
	if err != nil {
		panic(err)
	}

	content, err := os.ReadFile("localfile.bak")
	if err != nil {
		panic(err)
	}

	fmt.Printf("Downloaded content: %s\n", string(content))

	// Output:
	// Downloaded content: hello world
}

func ExampleCmd_builder() {
	// Construct a complex command using the fluent builder API.
	// This is often more readable than struct literals for commands with many options.
	cmd := invoke.Cmd("sh").
		Arg("-c").
		Arg("echo $GREETING").
		Env("GREETING", "hello builder").
		Dir("/tmp").
		Build()

	// Execute it
	env, _ := local.New()

	defer func() { _ = env.Close() }()

	ctx := context.Background()

	res, err := env.Run(ctx, cmd)
	if err != nil {
		panic(err)
	}

	// Note: output capture depends on explicit streams or RunBuffered,
	// but here we just verify the exit code.
	fmt.Printf("Exit Code: %d\n", res.ExitCode)

	// Output:
	// Exit Code: 0
}
