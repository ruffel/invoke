# invoke

A Go library for running commands across Local, SSH, and Docker environments using a single interface.

Instead of writing separate logic for `os/exec`, `crypto/ssh`, and the Docker SDK, write it once and swap the provider.

## Install

```bash
go get github.com/ruffel/invoke
```

Providers are separate modules:

```bash
go get github.com/ruffel/invoke/providers/local
go get github.com/ruffel/invoke/providers/ssh
go get github.com/ruffel/invoke/providers/docker
```

## Getting Started

| Provider | Setup | Target |
|----------|-------|--------|
| Local | `local.New()` | Host machine |
| SSH | `ssh.New(ssh.WithHost("10.0.0.1"), ...)` | Remote server |
| Docker | `docker.New(docker.WithContainerID("abc"))` | Running container |

Every provider implements `invoke.Environment` — all examples below work with any of them.

## Usage

### Run a command

```go
env, _ := local.New()
defer env.Close()

exec := invoke.NewExecutor(env)
result, _ := exec.RunBuffered(ctx, &invoke.Command{Cmd: "uptime"})
fmt.Println(string(result.Stdout))
```

### Shell one-liners

```go
result, _ := exec.RunShell(ctx, "ls -la | grep foo")
```

Or construct the command directly:

```go
cmd := invoke.ShellCommand("cat /etc/os-release | head -5")
result, _ := exec.RunBuffered(ctx, cmd)
```

### Stream output

```go
cmd := &invoke.Command{
    Cmd:    "make",
    Args:   []string{"build"},
    Stdout: os.Stdout,
    Stderr: os.Stderr,
}

_, err := env.Run(ctx, cmd)
```

### Live line-by-line streaming

```go
exec.RunLineStream(ctx, &invoke.Command{Cmd: "deploy"}, func(line string) {
    log.Println(line)
})
```

### SSH

```go
env, _ := ssh.New(
    ssh.WithHost("10.0.0.1"),
    ssh.WithUser("deploy"),
    ssh.WithKeyPath("~/.ssh/id_ed25519"),
)
defer env.Close()

// Same API as local
exec := invoke.NewExecutor(env)
result, _ := exec.RunBuffered(ctx, &invoke.Command{Cmd: "uptime"})
```

### Docker

```go
env, _ := docker.New(docker.WithContainerID("my-app"))
defer env.Close()

result, _ := env.Run(ctx, &invoke.Command{Cmd: "cat", Args: []string{"/etc/os-release"}})
```

### Sudo

```go
exec.Run(ctx, cmd, invoke.WithSudo())

// With options
exec.Run(ctx, cmd, invoke.WithSudo(
    invoke.WithSudoUser("postgres"),
    invoke.WithSudoPreserveEnv(),
))
```

### File transfers

```go
// Upload with permissions
env.Upload(ctx, "./configs/nginx", "/etc/nginx", invoke.WithPermissions(0o644))

// Download
env.Download(ctx, "/var/log/app.log", "./app.log")
```

### Interactive TTY

```go
cmd := invoke.ShellCommand("bash")
exec.RunInteractiveTTY(ctx, cmd)
```

### Retry on transport errors

```go
result, err := exec.Run(ctx, cmd, invoke.WithRetry(3, time.Second))
```

## Error Model

invoke distinguishes between two failure modes:

- **`ExitError`** — the command ran but exited non-zero. Contains the exit code and stderr. **Not retryable** — this is a definitive result.
- **`TransportError`** — the underlying transport failed (connection lost, daemon unreachable). **Retryable** via `WithRetry`.

```go
result, err := exec.Run(ctx, cmd)
if err != nil {
    var exitErr *invoke.ExitError
    if errors.As(err, &exitErr) {
        fmt.Printf("exit code %d: %s\n", exitErr.ExitCode, exitErr.Stderr)
    }
}
```

When `Wait()` returns an error, the `*Result` is still populated with the exit code and duration.

## Design

- **Streaming-first** — interfaces use `io.Reader` and `io.Writer`, not buffers
- **Context-aware** — all blocking operations respect `context.Context`
- **Secure defaults** — SSH host key checking is enforced; you must explicitly opt out
- **Provider-agnostic** — write once, deploy to local, SSH, or Docker

## License

MIT