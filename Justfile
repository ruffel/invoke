# Justfile for invoke

MODULES := "providers/local providers/docker providers/ssh providers/mock"

# Default recipe
default: test lint

# Run all tests with race detection
test:
    go test -race ./... $(for mod in {{MODULES}}; do echo "./$mod/..."; done)

# Run all tests WITHOUT race detection (for platforms where race is unsupported)
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
    for mod in {{MODULES}}; do \
        (cd $mod && go mod tidy); \
    done

# Check for clean git state after running fmt and tidy
check-clean: fmt tidy
    git diff --exit-code

# Build all examples
build-examples:
    go build ./examples/compat-check
    go build ./examples/unified-deploy

# Run compat check against local only
compat-check:
    go run ./examples/compat-check

# Run compat check against all providers
compat-check-all:
    go run ./examples/compat-check --all

# Run the unified-deploy demo
demo:
    go run ./examples/unified-deploy local
