# Changelog

All notable changes to `bastionhub` will be documented in this file.

Format roughly follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning is [SemVer](https://semver.org/) once we reach v1.0; until then, breaking changes can land in minor releases — see "Stability promises" in [README.md](README.md).

## [Unreleased]

### Added — enrollment for machines you don't control

- `bastionhub serve` — the invite/relay service, runs on the bastion VPS. Mints single-use invite codes with a TTL and a scope, serves a bootstrap script at `/j/<code>`, and relays a public key to the operator and a signed certificate back. **Holds no CA and signs nothing:** it is a rendezvous point, not a signing authority. Flags: `--bastion` (required), `--listen` (default `127.0.0.1:8420`), `--base-url`, `--tls-cert`/`--tls-key`. Admin token is generated on first run and stored 0600 at `~/.config/bastionhub/admin-token` (`$BASTIONHUB_ADMIN_TOKEN_FILE`); invite state lives at `~/.config/bastionhub/invites.json` (`$BASTIONHUB_SERVE_STATE`).
- `bastionhub invite <name>` — operator-side. Mints an invite, prints the one-line command to read to whoever is on site, waits for their public key, shows its fingerprint for out-of-band confirmation, signs locally via `sshca cert sign`, sends the certificate back, and registers the endpoint. Flags: `--url`/`$BASTIONHUB_SERVE_URL`, `--admin-token`/`$BASTIONHUB_ADMIN_TOKEN`, `--shape`, `--port`, `--principal`, `--valid`, `--ttl`, `--user`, `--identity`, `--description`, `--ca-dir`, `--yes`.
- Bootstrap scripts for sh and PowerShell, served per-invite. They require only `ssh`, `ssh-keygen` and `curl`/`iwr` — nothing to install, no admin rights. `--shape device` installs a systemd unit, launchd agent, or scheduled task around plain `ssh` (`ServerAliveInterval` replaces autossh's dead-peer detection). `--shape session` runs in the foreground and removes its key directory on exit.
- Windows is now supported as a far end via `invite`, though not via `endpoint install/setup`. The scheduled-task path avoids the NSSM + autossh-on-Windows problem that kept it out of scope before.

### Security properties

- The service stores public keys and certificates only. A full compromise of the bastion yields public material and expired codes; it cannot mint a certificate.
- Invite codes are 8 characters from a 31-symbol alphabet with no `0`/`O`/`1`/`I`/`L` (~40 bits), drawn with `crypto/rand` using rejection sampling to keep the distribution uniform. Codes get read aloud, so they normalize case and separators.
- Codes are single-use and TTL'd (default 30 min). Unknown, expired, spent and revoked codes are all answered identically, so probing cannot distinguish them. Repeated failures from one source are rate-limited.
- The first public key submitted for an invite wins; a later, different key is rejected, so the fingerprint the operator confirmed out-of-band stays the key that gets signed.
- The admin token is compared in constant time. A submission containing `PRIVATE KEY` is rejected outright.

## [0.1.0] — 2026-05-29

Initial release. Self-hosted SSH bastion + reverse-tunnel substrate. Pairs with [sshca](https://github.com/roselabs-io/sshca) for cert auth.

### Added — Engineer-side commands

- `bastionhub init` — creates a starter `~/.config/bastionhub/endpoints.yaml` (override path via `$BASTIONHUB_CONFIG`).
- `bastionhub list` — tabular view of configured endpoints.
- `bastionhub status` — SSH to the bastion (`admin_alias`) to check which endpoints have a live reverse-tunnel listener; shows UP/DOWN + uptime.
- `bastionhub ssh <endpoint>` — ProxyJump via the bastion. Passes `-i <identity> -o IdentitiesOnly=yes` when the endpoint has an `identity:` field configured (avoids `MaxAuthTries` blow-out). Cross-platform: works on Linux, macOS, and Windows.
- `bastionhub endpoint register <name>` — adds an endpoint to the registry. Auto-allocates a port from `12001-12099` if `--port` not specified.
- `bastionhub endpoint unregister <name>` — removes from registry. Does NOT revoke certs (use `sshca cert revoke` separately).
- `bastionhub endpoint enroll <name> --pubkey-file <path>` — one-shot: shells out to `sshca cert sign` with principal `gw-tunnel` (overridable via `--principal`) and validity `+52w` (overridable via `--valid`), then adds to the registry.

### Added — Endpoint-side commands (Linux + macOS)

- `bastionhub endpoint install` — dispatches on `runtime.GOOS`:
  - Linux (needs root): installs autossh via apt/dnf/yum/apk, generates `/etc/bastionhub-tunnel/id_ed25519`, prints the pubkey.
  - macOS (per-user, no sudo): detects brew, `brew install autossh` if missing, generates `~/.bastionhub-tunnel/id_ed25519`, prints the pubkey.
- `bastionhub endpoint setup --port N --bastion HOST` — dispatches:
  - Linux: writes `/etc/systemd/system/bastionhub-tunnel.service`, runs `systemctl enable --now`.
  - macOS: writes `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist`, runs `launchctl bootstrap gui/$UID`.
  - Both support `--dry-run` (print the unit/plist + commands without writing or loading).

### Added — Bastion-side deploy artifacts

In [`deploy/bastion/`](deploy/bastion/) — ship these to the bastion VPS during setup (see the directory's README for the procedure).

- `10-bastionhub.conf` — foundational sshd drop-in: `TrustedUserCAKeys`, `RevokedKeys`, optional `HostCertificate`, plus `Match User gw-tunnel` (with `PermitListen 12001-12099`) and `Match User gw-user`.
- `30-passthrough-acl.conf` + `principal-to-acl.sh` — optional Pattern B for per-principal `permitopen` scoping via `AuthorizedPrincipalsCommand`.

### Added — Cross-platform

- Single Go binary for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, `windows/arm64`.
- Engineer-side commands work on all six. Endpoint-side `install` / `setup` are Linux + macOS only (Windows-as-endpoint is intentionally out of scope).

### Added — Distribution

- GitHub Actions CI: build + vet matrix on every push/PR across all six platforms.
- GitHub Actions release pipeline: on `v*` tag push, builds the six-platform matrix with `-ldflags "-X main.version=$TAG"` injection, packages as `.tar.gz` (Unix) / `.zip` (Windows), generates SHA-256 checksums, and attaches everything to an auto-released GitHub Release.

### Added — Tests

- 8 unit + regression tests covering `sanitizeForComment`, `loadConfig` (including a regression test for the nil-map panic discovered during initial deploys when `endpoints:` has only commented children), and `allocatePort` (empty / skip-taken / fill-holes / exhausted).

[0.1.0]: https://github.com/roselabs-io/bastionhub/releases/tag/v0.1.0
