# Justfile for invoke.
#
# CI runs these same commands directly (see .github/workflows/ci.yml) to
# avoid a third-party setup action; keep the two in sync.

set shell := ["bash", "-euo", "pipefail", "-c"]

# Full local verification: mirrors what CI runs.
default: check

# Format-check, lint, tidy-check, cross-build, race tests.
check: fmt-check lint tidy-check build-windows test-race

# Build all packages, in every module.
build:
    go build ./...
    cd docker && go build ./...

# Cross-build for Windows: everything must compile there, even though
# execution semantics are POSIX-only this cycle.
build-windows:
    GOOS=windows go build ./...
    cd docker && GOOS=windows go build ./...

# Run tests. The docker provider's contracts need a daemon and are behind
# a build tag; see test-docker.
test:
    go test ./...
    cd docker && go test ./...

# Run tests with the race detector.
test-race:
    go test -race ./...
    cd docker && go test -race ./...

# Run the docker provider's contract suite against a real container.
# Requires a reachable daemon, so it is not part of `check`.
test-docker:
    cd docker && go test -tags docker -race -timeout 15m ./...

# Run linters across every module.
lint:
    golangci-lint run ./...
    cd docker && golangci-lint run ./...

# Apply the configured formatters (gofumpt, gci).
fmt:
    golangci-lint fmt ./...
    cd docker && golangci-lint fmt ./...

# Fail if any file needs formatting.
fmt-check:
    golangci-lint fmt --diff ./...
    cd docker && golangci-lint fmt --diff ./...

# Tidy go.mod/go.sum.
#
# The docker module is omitted until the core module is released: it
# resolves the core module through the workspace, and tidy would try to
# fetch a version that does not exist yet.
tidy:
    go mod tidy

# Fail if go.mod/go.sum need tidying.
tidy-check:
    go mod tidy -diff
