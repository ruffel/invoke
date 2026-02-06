# invoke

`invoke` is a Go library for unified command execution. It lets you run commands across **Local**, **SSH**, and **Docker** environments using a single interface.

It solves the "three codepaths" problem: instead of writing separate logic for `os/exec`, `crypto/ssh`, and the Docker SDK, you write your logic once and `invoke` handles the transport.

## Why?

Running commands properly is annoying.
- **Local**: `os/exec` is fine, but boilerplate heavy.
- **SSH**: `crypto/ssh` is low-level; you're manually managing sessions and streams.
- **Docker**: The SDK is verbose and requires stream demultiplexing.

`invoke` abstracts this all away.

## Installation

```bash
go get github.com/ruffel/invoke
```

## Quick Start

### 1. The Basics
Run a command locally, or on a remote server, just by swapping the provider.

```go
ctx := context.Background()

// env := local.New()
env, _ := ssh.New(ssh.NewConfig("10.0.0.1", "root"))
defer env.Close()

// The Executor wrapper gives you nice helpers (buffering, sudo, etc)
exec := invoke.NewExecutor(env)

// Runs 'uptime' on the remote host
out, _ := exec.RunBuffered(ctx, &invoke.Command{Cmd: "uptime"})
fmt.Println(string(out.Stdout))
```

### 2. Streaming Output (The "Real World" Use Case)
For long-running jobs (builds, deploys), you don't want to buffer everything. `invoke` is streaming-first.

```go
cmd := invoke.Command{
    Cmd: "docker",
    Args: []string{"build", "."},
    Stdout: os.Stdout, // Stream directly to your terminal
    Stderr: os.Stderr,
}

err := env.Run(ctx, cmd)
```

### 3. Sudo without the headache
We handle the `sudo -n` flags and prompt avoidance for you.

```go
// Automatically wraps as: sudo -n -- apt-get update
exec.Run(ctx, cmd, invoke.WithSudo())
```

### 4. File Transfer
Uploads and downloads work recursively and consistently across all providers.

```go
// Upload a local directory to a remote server
err := env.Upload(ctx, "./configs/nginx", "/etc/nginx", invoke.WithPermissions(0644))
```

### 5. Convenience (One-Liners)
For simple local scripts, you don't need to manually manage the environment.

```go
// Run a shell one-liner
res, _ := local.RunShell(ctx, "ls -la | grep foo")

// Run a configured command
res, _ := local.RunCommand(ctx, &invoke.Command{Cmd: "ls", Dir: "/tmp"})
```

### 6. Fluent Builder
Construct commands without struct literals.

```go
cmd := invoke.Cmd("docker").
    Arg("run").
    Arg("-it").
    Env("GOOS", "linux").
    Dir("/app").
    Build()

exec.Run(ctx, cmd)
```

## Design Philosophy

- **Streaming First**: We avoid buffering whenever possible. Interfaces use `io.Reader` and `io.Writer`.
- **Context Aware**: All blocking operations accept `context.Context`. Cancellation works as you expect (sending signals to remote processes).
- **Secure Defaults**: SSH host key checking is enforced by default. You have to opt-out explicitly.

## Troubleshooting

### "configuration error: HostKeyCheck is missing"
When using the SSH provider, you must verify the server's identity to prevent Man-in-the-Middle attacks.
- **Production**: Provide a `HostKeyCallback` (e.g., parse your `~/.ssh/known_hosts` file).
- **Testing**: Use `c.InsecureSkipVerify = true` to disable check (NOT recommended for production).

### "failed to dial ssh: ... handshake failed"
- Check that your SSH key has the correct permissions (`chmod 600`).
- Ensure the remote user exists and allows SSH login.
- Verify `AllowUsers` or `PermitRootLogin` in the server's `sshd_config`.

## License

MIT