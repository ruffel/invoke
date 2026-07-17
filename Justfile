# Justfile for invoke.
#
# CI runs these same commands directly (see .github/workflows/ci.yml) to
# avoid a third-party setup action; keep the two in sync.

set shell := ["bash", "-euo", "pipefail", "-c"]

# Full local verification: mirrors what CI runs.
default: check

# Format-check, lint, tidy-check, cross-build, race tests.
check: fmt-check lint tidy-check build-windows test-race

# Build all packages.
build:
    go build ./...

# Cross-build for Windows: everything must compile there, even though
# execution semantics are POSIX-only this cycle.
build-windows:
    GOOS=windows go build ./...

# Run tests.
test:
    go test ./...

# Run tests with the race detector.
test-race:
    go test -race ./...

# Run linters.
lint:
    golangci-lint run ./...

# Apply the configured formatters (gofumpt, gci).
fmt:
    golangci-lint fmt ./...

# Fail if any file needs formatting.
fmt-check:
    golangci-lint fmt --diff ./...

# Tidy go.mod/go.sum.
tidy:
    go mod tidy

# Fail if go.mod/go.sum need tidying.
tidy-check:
    go mod tidy -diff
