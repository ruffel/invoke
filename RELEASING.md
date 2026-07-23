# Releasing

This repository holds two modules:

- `github.com/ruffel/invoke` — the core module, tagged `vX.Y.Z`
- `github.com/ruffel/invoke/docker` — the Docker provider, tagged
  `docker/vX.Y.Z`

They are released together and share a version number.

## Why it takes two stages

The submodule depends on the core module, and Go resolves that dependency
by version rather than by directory. During development the two find each
other through `go.work`, which a consumer does not have; what a consumer
gets is whatever `docker/go.mod` names.

So the core module has to exist at a version before the submodule can
point at it. The order is therefore fixed:

1. Tag the core module.
2. Repoint `docker/go.mod` at that tag, and commit.
3. Tag the submodule at that commit.

Skipping step 2 publishes a submodule that compiles in this repository
and nowhere else, because it would still name the *previous* release —
carrying the previous API. `just release-check` exists to make that
mistake impossible; run it before tagging.

## Procedure

Replace `v0.2.0` throughout with the version being released.

```sh
# 0. Everything green, including the lanes that need a container runtime.
just check
just test-integration

# 1. Tag the core module.
git tag v0.2.0
git push ruffel v0.2.0

# 2. Repoint the submodule, tidy it, and commit.
#
# Tidying only becomes possible here: until the core module is published
# there is no version for the submodule to resolve, which is why its
# go.mod has been maintained by hand until now.
cd docker
go mod edit -require=github.com/ruffel/invoke@v0.2.0
GOWORK=off go mod tidy
cd ..
just release-check v0.2.0          # refuses if the pin is wrong
git commit -am "build(docker): Require the core module at v0.2.0"
git push ruffel main

# 3. Tag the submodule.
git tag docker/v0.2.0
git push ruffel docker/v0.2.0
```

## Verifying the release

Tags are effectively permanent once the module proxy has seen them, so
check from outside the repository rather than trusting the local build:

```sh
cd "$(mktemp -d)"
go mod init example.com/check
go get github.com/ruffel/invoke@v0.2.0
go get github.com/ruffel/invoke/docker@v0.2.0
cat > main.go <<'EOF'
package main

import (
	_ "github.com/ruffel/invoke"
	_ "github.com/ruffel/invoke/docker"
	_ "github.com/ruffel/invoke/local"
	_ "github.com/ruffel/invoke/ssh"
)

func main() {}
EOF
go build ./...
```

The `release-verify` workflow does exactly this on every tag push, so it
runs whether or not anyone remembers to.

If it fails, the tag cannot be withdrawn — the proxy keeps serving it.
Publish a corrected patch version instead, and add the bad version to
the `retract` block in go.mod so the go command steers consumers off it.

## History on the proxy

Versions up to `v0.1.0` were published under an older layout, with
providers in `providers/*` submodules. Those submodule tags were later
deleted and the proxy never cached them, so the old versions resolve but
do not build: `go get github.com/ruffel/invoke@v0.1.0` succeeds, and the
consumer's next `go mod tidy` fails asking for `providers/*` revisions
that no longer exist anywhere.

`go.mod` therefore retracts `[v0.0.1, v0.1.0]`. The go command reads
retractions from the latest release, then hides those versions from
version lists and warns anyone already on them — so the directive takes
effect with the first release that carries it. The `providers/*` paths
themselves cannot be retracted: retraction is a statement a module makes
about its own versions, and no new version of those paths will ever
exist to make it.
