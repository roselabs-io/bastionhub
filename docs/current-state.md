# Current state

> What works right now in `bastionhub`. Updated on every change.

## Status

**v0.1.0-dev** — core endpoint management + endpoint-side install / setup implemented. Single ~5 MB binary, two deps (`urfave/cli/v3` v3.9.0 + `gopkg.in/yaml.v3` v3.0.1). Requires `sshca` in `PATH` for cert operations.

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
  perso-mbp:
    port: 12001
    user: psd
    identity: ~/.ssh/id_ed25519
    description: "Personal MacBook"
```

The schema is part of bastionhub's contract surface — upstream tools (e.g. the `gateway` product) compile their richer registries down to this format.

## Recently landed

- **2026-05-29** — v0.1.0-dev: repo created from the three-repo decomposition (upstream ADR-007/008). Cert code moved out (now in `sshca`); bastion-substrate code moved in from `gateway/cli/main.go`. Renames during migration:
  - `Gateway` struct → `Endpoint` (substrate-neutral; "gateway" is OT-vocabulary that belongs upstream)
  - `gateways.yaml` → `endpoints.yaml`
  - `gateway-tunnel.service` (systemd) → `bastionhub-tunnel.service`
  - `/etc/gateway-tunnel/` paths → `/etc/bastionhub-tunnel/`
  - `gwctl gateway *` commands → `bastionhub endpoint *`
  - `GWCTL_CONFIG` env var → `BASTIONHUB_CONFIG`
  - `gatewayEnrollCmd` rewritten to shell out to `sshca cert sign` (was in-process signing)
- **2026-05-29** — Pattern B deploy artifacts migrated from `gateway/deploy/bastion/` — script + sshd drop-in + README.

## What's NOT here yet

- **CI/CD + Homebrew tap.** Builds are validated locally for darwin-arm64, windows-amd64, windows-arm64, linux-amd64. GitHub Actions release pipeline + a `roselabs-io/homebrew-tools` tap is the next cross-cutting infra item (shared with `sshca`).
- **`docs/reference/` content** — SSH bastion + reverse-tunnel primer, contract docs, Pattern B walk-through. Backlog.
- **CI/CD** — no GitHub Actions yet.
- **Distribution** — no Homebrew tap; install from source only.
## Recently landed

- **2026-05-29** — [docs/reference/contracts.md](reference/contracts.md) shipped. Declares the three semver-disciplined surfaces downstream consumers (the gateway product, future tools) depend on: CLI grammar, `endpoints.yaml` schema, deploy artifact paths (systemd unit name, launchd plist label, sshd drop-in filenames). Documents the unidirectional registry → `endpoints.yaml` compilation contract for upstream tools and the cross-tool versioning relationship with sshca.
- **2026-05-29** — Cross-platform engineer-laptop support (Windows). Replaced `syscall.Exec` (Unix-only) in `sshCmd` with `exec.Command + Run()` using inherited stdio. Exit-code propagation preserved (verified: `true` → 0, `false` → 1, `exit 42` → 42). Cross-compiles cleanly for windows/amd64 (6.1 MB), windows/arm64 (5.8 MB), linux/amd64 (6.0 MB), darwin/arm64. Engineer-side commands (`list`, `status`, `ssh`, `endpoint register/enroll/unregister`) now work on Windows. Endpoint-side (`install`/`setup` for autossh-as-service) remains parked for Windows per upstream architectural answer (V1 hardware bridges to Windows devices behind the gateway, not Windows-as-endpoint).
- **2026-05-29** — macOS endpoint via launchd shipped. `bastionhub endpoint install` and `setup` now dispatch on `runtime.GOOS`: Linux gets the existing systemd + apt/dnf/yum/apk path; macOS gets a new launchd + brew + per-user `~/.bastionhub-tunnel/` + `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist` path. Setup gains a `--dry-run` flag (print the unit/plist + commands without writing or loading) — useful when a tunnel is already running and you want to inspect the generated config without disrupting it. Verified end-to-end on perso-mbp: install created `~/.bastionhub-tunnel/` + ed25519 key; `setup --dry-run` produced a valid plist with Apple Silicon brew prefix (`/opt/homebrew/bin/autossh`), per-user paths throughout, all launchd keys present. Patrick's existing hand-written plist untouched (additive migration policy).
- **2026-05-29** — Foundational sshd drop-in shipped: `deploy/bastion/10-bastionhub.conf` (CA trust + `Match User gw-tunnel` + `Match User gw-user`). Combined with the existing optional Pattern B drop-in, a fresh bastion can now be brought up from `git clone` per the procedure in [`deploy/bastion/README.md`](../../deploy/bastion/README.md) — no more "lives manually on Patrick's bastion" caveat for the substrate-completeness story.
- **2026-05-29** — Live deployment cutover from `gwctl`. `bastionhub` binary installed at `~/.local/bin/bastionhub` on Patrick's perso-mbp; `~/.config/bastionhub/endpoints.yaml` migrated from `~/.config/gwctl/gateways.yaml` (3 endpoints: customer-002 placeholder, perso-mbp, work-laptop). End-to-end verified live: `bastionhub list/status` query the real bastion at `46.225.2.150`; `bastionhub ssh perso-mbp -- uptime` round-tripped successfully via cert auth. Old `gwctl` binary removed.

## See also

- [decisions/inherited.md](decisions/inherited.md) — upstream decisions
- [planning/backlog.md](planning/backlog.md) — what's coming next
- [../deploy/bastion/](../deploy/bastion/) — bastion-VPS install artifacts
