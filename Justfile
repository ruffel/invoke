# Justfile for invoke.
#
# CI runs these same commands directly (see .github/workflows/ci.yml) to
# avoid a third-party setup action; keep the two in sync.

set shell := ["bash", "-euo", "pipefail", "-c"]

# Full local verification: mirrors what CI runs.
default: check

# Format-check, lint, tidy-check, cross-build, race tests.
check: workspace-check fmt-check lint tidy-check build-windows test-race

# Fail if the workspace demands a newer toolchain than the modules do.
#
# `go work init` and `go work sync` stamp the running toolchain's exact
# patch version into go.work. Whoever runs them on a newer Go than the
# modules declare makes the tree unbuildable for everyone else — including
# CI, which installs the version go.mod names.
workspace-check:
    #!/usr/bin/env bash
    set -euo pipefail

    workspace=$(awk '/^go /{print $2; exit}' go.work)
    module=$(awk '/^go /{print $2; exit}' go.mod)

    if [[ "$workspace" != "$module" ]]; then
        echo "go.work declares go $workspace but go.mod declares go $module" >&2
        echo "run: go work edit -go=$module" >&2
        exit 1
    fi

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

# Run the SSH provider's contract suite against a real OpenSSH server in a
# container. The in-process server used by the unit lane implements what
# this repository believes the protocol does; only a real one can show
# where that belief is wrong. Requires a container runtime.
test-openssh:
    go test -tags openssh -race -timeout 15m ./ssh/

# Compare the providers against each other. The contract suite asks
# whether each obeys a stated rule; this asks whether they agree on
# everything else. Needs a container runtime for the ssh and docker
# targets, so it is part of the integration lanes rather than `check`.
test-parity:
    cd docker && go test -tags docker -race -timeout 15m -run TestProviderParity ./...

# Every integration lane: all need a container runtime, so none is part
# of `check`.
test-integration: test-docker test-openssh

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

# Tidy go.mod/go.sum in both modules.
#
# The submodule is tidied with the workspace off, the way a consumer
# resolves it: against the published core module its go.mod names, not the
# local checkout the workspace would substitute. That version now exists,
# so the submodule is no longer maintained by hand.
tidy:
    go mod tidy
    cd docker && GOWORK=off go mod tidy

# Fail if either module's go.mod/go.sum needs tidying.
tidy-check:
    go mod tidy -diff
    cd docker && GOWORK=off go mod tidy -diff

# Check the tree is ready to be tagged as VERSION (e.g. v0.2.0).
#
# The submodule pins the core module by version, and during development
# that pin points at whatever already exists — which is the previous
# release, carrying the previous API. Tagging without repointing it would
# publish a submodule that compiles here and nowhere else. This refuses to
# let that happen.
release-check VERSION:
    #!/usr/bin/env bash
    set -euo pipefail

    if [[ ! "{{VERSION}}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo "version must look like v1.2.3, got {{VERSION}}" >&2
        exit 1
    fi

    pinned=$(go mod edit -json docker/go.mod \
        | python3 -c 'import json,sys; print(next((r["Version"] for r in (json.load(sys.stdin).get("Require") or []) if r["Path"]=="github.com/ruffel/invoke"), ""))')

    if [[ "$pinned" != "{{VERSION}}" ]]; then
        echo "docker/go.mod requires github.com/ruffel/invoke ${pinned:-<nothing>}, expected {{VERSION}}" >&2
        echo "run: cd docker && go mod edit -require=github.com/ruffel/invoke@{{VERSION}}" >&2
        exit 1
    fi

    for f in go.mod docker/go.mod; do
        if grep -qE '^\s*replace ' "$f"; then
            echo "$f contains a replace directive, which a released module must not" >&2
            exit 1
        fi
    done

    echo "ready to tag {{VERSION}} and docker/{{VERSION}}"
