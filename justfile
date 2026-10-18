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

# The offline secret scan: every file of `git ls-files`, scanned with detect-secrets 1.5.0 (run by uv) against
# `.secrets.baseline`. A secret the baseline does not hold is printed as `path:line: type` and fails the recipe.
# The baseline is read, never rewritten; AGENTS.md (Commands) gives the command that updates it.
secrets:
    #!/usr/bin/env -S uv run --quiet --script
    # /// script
    # requires-python = ">=3.9"
    # dependencies = ["detect-secrets==1.5.0"]
    # ///
    import subprocess
    import sys

    from detect_secrets import SecretsCollection
    from detect_secrets.core import baseline
    from detect_secrets.settings import transient_settings

    # `scan_files` uses a multiprocessing pool, whose workers re-import this script where processes are spawned (macOS).
    if __name__ == "__main__":
        tracked = subprocess.run(["git", "ls-files", "-z"], capture_output=True, text=True, check=True).stdout
        with transient_settings({}):
            accepted = baseline.load(baseline.load_from_file(".secrets.baseline"), ".secrets.baseline")
            scanned = SecretsCollection()
            scanned.scan_files(*filter(None, tracked.split("\0")))
            found = sorted(f"{path}:{secret.line_number}: {secret.type}" for path, secret in scanned - accepted)
        for finding in found:
            print(finding)
        if found:
            sys.exit(f"{len(found)} secrets not in .secrets.baseline")

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
gate: check secrets test build

# `govulncheck` against the Go vulnerability database; needs the network, so it is not part of the gate.
audit:
    go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

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
