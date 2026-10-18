# Go CLI recipes of the command contract. `check` and `gate` are what the local preflight runs.

# The six release targets of `release.yml` (the build matrix of `ci.yml`).
targets := "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

# Static checks: formatting, and `go vet` for each target. The code selects Linux-only and Darwin-only
# paths with `runtime.GOOS`, so each target is vetted and compiled.
check:
    #!/usr/bin/env sh
    set -eu
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then echo "gofmt: unformatted files:" >&2; echo "$unformatted" >&2; exit 1; fi
    for target in {{targets}}; do
        echo "go vet $target"
        GOOS="${target%/*}" GOARCH="${target#*/}" go vet ./...
    done

# The test suite.
test:
    go test ./...

# `go build` with CGO_ENABLED=0 for each target. The binaries are written to a temporary directory and removed.
build:
    #!/usr/bin/env sh
    set -eu
    out="$(mktemp -d)"
    trap 'rm -rf "$out"' EXIT
    for target in {{targets}}; do
        echo "go build $target"
        CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build -o "$out/bastionhub-${target%/*}-${target#*/}" .
    done

# The deterministic gate CI runs.
gate: check test build

# Apply the formatter.
fix:
    gofmt -w .

# Switch to main and fast-forward it from origin; refuses local changes or a diverged main.
ff:
    #!/usr/bin/env sh
    set -eu
    if [ -n "$(git status --porcelain)" ]; then echo "ff: the working tree has changes" >&2; exit 1; fi
    git switch main
    git fetch origin main
    git merge --ff-only origin/main
