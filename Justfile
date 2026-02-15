# Justfile for invoke

MODULES := "examples/compat-check examples/unified-deploy providers/local providers/docker providers/ssh providers/mock"

# Default recipe
default: test lint

# Run all tests with race detection
test:
    go test -race ./... $(for mod in {{MODULES}}; do echo "./$mod/..."; done)

# Run all tests WITHOUT race detection (for Windows/platforms where race is unsupported)
test-no-race:
    go test ./... $(for mod in {{MODULES}}; do echo "./$mod/..."; done)

# Run linters
lint:
    golangci-lint run ./... $(for mod in {{MODULES}}; do echo "./$mod/..."; done)

# Run integration tests (locally)
test-integration:
    go test -race -v -tags=integration ./... $(for mod in {{MODULES}}; do echo "./$mod/..."; done)

# Clean build artifacts
clean:
    go clean

# Run go fmt on all modules
fmt:
    go fmt ./...
    for mod in {{MODULES}}; do \
        (cd $mod && go fmt ./...); \
    done

# Run go mod tidy on all modules
tidy:
    go mod tidy
    (cd examples/unified-deploy && go mod tidy)
    for mod in {{MODULES}}; do \
        (cd $mod && go mod tidy); \
    done

# Check for clean git state after running fmt and tidy
check-clean: fmt tidy
    git diff --exit-code

# Run the unified-deploy example (pass args, e.g. "local" or "ssh --ephemeral")
demo +args="":
    go run ./examples/unified-deploy {{args}}

# Run against local target
demo-local +args="":
    just demo local {{args}}

# Run against SSH target (ephemeral)
demo-ssh +args="":
    just demo ssh --ephemeral {{args}}

# Run against Docker target (ephemeral)
demo-docker +args="":
    just demo docker --ephemeral {{args}}
