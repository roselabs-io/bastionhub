# Current state

> What works right now in `bastionhub`. Updated on every change.

## Status

**v0.1.0** — core endpoint management + endpoint-side install / setup implemented. Single ~5 MB binary, two deps (`urfave/cli/v3` v3.9.0 + `gopkg.in/yaml.v3` v3.0.1). Requires `sshca` in `PATH` for cert operations.

## What works

**Daily use (engineer laptop):**

- `bastionhub init` — creates a starter `~/.config/bastionhub/endpoints.yaml`
- `bastionhub list` — lists configured endpoints
- `bastionhub status` — queries the bastion's `admin_alias` to show which endpoints have a live reverse tunnel
- `bastionhub ssh <endpoint>` — ProxyJump via bastion to the named endpoint (passes `-i` + `IdentitiesOnly=yes` when an `identity:` is configured)

**Endpoint management (engineer laptop):**

- `bastionhub endpoint register <name> [--user] [--port] [--identity] [--description]` — adds to `endpoints.yaml`
- `bastionhub endpoint unregister <name>` — removes
- `bastionhub endpoint enroll <name> --pubkey-file <path>` — one-shot: shells out to `sshca cert sign` with `--principal gw-tunnel --valid +52w`, then registers

**Endpoint device (Linux + macOS):**

- **Linux** (needs root):
  - `sudo bastionhub endpoint install` — installs autossh (apt/dnf/yum/apk), generates an ed25519 key at `/etc/bastionhub-tunnel/id_ed25519`, prints the pubkey
  - `sudo bastionhub endpoint setup --port N --bastion HOST` — writes `/etc/systemd/system/bastionhub-tunnel.service` and starts it
- **macOS** (per-user, no sudo):
  - `bastionhub endpoint install` — detects brew prefix, `brew install autossh` if needed, generates an ed25519 key at `~/.bastionhub-tunnel/id_ed25519`, prints the pubkey
  - `bastionhub endpoint setup --port N --bastion HOST` — writes `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist`, `launchctl bootstrap`s it, prints status
  - Both support `--dry-run` for setup (print the unit/plist + commands without writing or loading — useful when a tunnel is already running and you want to inspect the generated config)

**Bastion-side deploy artifacts** (manual install on the bastion VPS — see [`deploy/bastion/README.md`](../../deploy/bastion/README.md) for the procedure):

- [`deploy/bastion/10-bastionhub.conf`](../../deploy/bastion/10-bastionhub.conf) — **foundational** sshd drop-in: `TrustedUserCAKeys`, `RevokedKeys`, optional `HostCertificate`, `Match User gw-tunnel` (with `PermitListen 12001-12099`), `Match User gw-user`. Sufficient for the V0 substrate by itself.
- [`deploy/bastion/principal-to-acl.sh`](../../deploy/bastion/principal-to-acl.sh) — **optional Pattern B**: `AuthorizedPrincipalsCommand` script for the `gw-passthrough` Unix user (per-principal `permitopen` scoping)
- [`deploy/bastion/30-passthrough-acl.conf`](../../deploy/bastion/30-passthrough-acl.conf) — **optional Pattern B**: sshd drop-in wiring the script

## Config

`~/.config/bastionhub/endpoints.yaml` (override with `$BASTIONHUB_CONFIG`):

```yaml
bastion_alias: bastion         # ProxyJump target for daily use
admin_alias: bastion-root      # for status/admin queries

endpoints:
  my-laptop:
    port: 12001
    user: alice
    identity: ~/.ssh/id_ed25519
    description: "Example endpoint"
```

The schema is part of bastionhub's contract surface — upstream tools that produce richer registries compile them down to this format.

## What's NOT here yet

- **`docs/reference/` content** — SSH bastion + reverse-tunnel primer, Pattern B walk-through. Backlog.

## Recently landed

- **2026-05-29** — v0.1.0 tagged. First GitHub Release created via `.github/workflows/release.yml`: 6 platform binaries (linux/darwin/windows × amd64/arm64) + sha256 checksums attached, version injected via `-ldflags "-X main.version=v0.1.0"`.
- **2026-05-29** — Release pipeline + unit tests shipped. `.github/workflows/release.yml` triggered on `v*` tag push builds the 6-platform matrix with ldflags injection, packages each binary as `.tar.gz` (Unix) / `.zip` (Windows), generates SHA-256 checksums, and attaches everything to an auto-released GitHub Release. `main.go`'s `version` flipped from `const` to `var` to enable the injection. 8 unit + regression tests in `main_test.go` cover `sanitizeForComment`, `loadConfig` (including a regression for the nil-map panic discovered during initial deploys), and `allocatePort` (empty / skip-taken / fill-holes / exhausted).
- **2026-05-29** — GitHub Actions CI shipped at `.github/workflows/ci.yml`. 6-platform matrix runs `go vet` + `go build` per cell on every push to main and every PR. `go test ./...` runs on linux/amd64.
- **2026-05-29** — [docs/reference/contracts.md](reference/contracts.md) shipped. Declares the three semver-disciplined surfaces downstream consumers depend on: CLI grammar, `endpoints.yaml` schema, deploy artifact paths (systemd unit name, launchd plist label, sshd drop-in filenames).
- **2026-05-29** — Cross-platform engineer-laptop support (Windows). Replaced `syscall.Exec` (Unix-only) in `sshCmd` with `exec.Command + Run()` using inherited stdio. Exit-code propagation preserved. Cross-compiles cleanly for windows/amd64, windows/arm64, linux/amd64, darwin/arm64. Engineer-side commands (`list`, `status`, `ssh`, `endpoint register/enroll/unregister`) work on Windows.
- **2026-05-29** — macOS endpoint via launchd shipped. `bastionhub endpoint install` and `setup` dispatch on `runtime.GOOS`: Linux uses systemd + apt/dnf/yum/apk; macOS uses launchd + brew + per-user `~/.bastionhub-tunnel/` + `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist`. Setup gains a `--dry-run` flag (print the unit/plist + commands without writing or loading) — useful when a tunnel is already running and you want to inspect the generated config without disrupting it. Validated end-to-end on macOS (Apple Silicon): install created the key dir + ed25519 key; `setup --dry-run` produced a valid plist with brew-prefix autossh path, per-user paths, all launchd keys present. Migration policy is additive — existing hand-written plists keep running.
- **2026-05-29** — Foundational sshd drop-in shipped: `deploy/bastion/10-bastionhub.conf` (CA trust + `Match User gw-tunnel` + `Match User gw-user`). Combined with the optional Pattern B drop-in, a fresh bastion can be brought up from `git clone` per the procedure in [`deploy/bastion/README.md`](../../deploy/bastion/README.md).
- **2026-05-29** — v0.1.0-dev migration from the upstream gateway monorepo. Cert code moved to `sshca`; bastion-substrate code moved here. Renames during migration: `Gateway` struct → `Endpoint`, `gateways.yaml` → `endpoints.yaml`, `gateway-tunnel.service` → `bastionhub-tunnel.service`, `/etc/gateway-tunnel/` → `/etc/bastionhub-tunnel/`, `gwctl gateway *` → `bastionhub endpoint *`. `endpoint enroll` rewritten to shell out to `sshca cert sign` (no longer in-process signing).
- **2026-05-29** — Pattern B deploy artifacts migrated: script + sshd drop-in + README.

## See also

- [decisions/inherited.md](decisions/inherited.md) — upstream decisions
- [planning/backlog.md](planning/backlog.md) — what's coming next
- [../deploy/bastion/](../deploy/bastion/) — bastion-VPS install artifacts
