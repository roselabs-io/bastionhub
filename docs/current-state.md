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

**Endpoint device (Linux only, needs root):**

- `sudo bastionhub endpoint install` — installs autossh (apt/dnf/yum/apk), generates an ed25519 key at `/etc/bastionhub-tunnel/id_ed25519`, prints the pubkey
- `sudo bastionhub endpoint setup --port N --bastion HOST` — writes `/etc/systemd/system/bastionhub-tunnel.service` and starts it

**Bastion-side deploy artifacts** (manual install on the bastion VPS):

- [`deploy/bastion/principal-to-acl.sh`](../../deploy/bastion/principal-to-acl.sh) — `AuthorizedPrincipalsCommand` script for the `gw-passthrough` Unix user (Pattern B)
- [`deploy/bastion/30-passthrough-acl.conf`](../../deploy/bastion/30-passthrough-acl.conf) — sshd drop-in that wires the script
- [`deploy/bastion/README.md`](../../deploy/bastion/README.md) — install procedure

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

- **Bastion-side starter sshd config** (the `Match User gw-tunnel`, `Match User gw-user`, `TrustedUserCAKeys`, `RevokedKeys` blocks). Currently lives on Patrick's bastion as a manual setup; needs to ship as a documented `00-bastionhub.conf` drop-in. Backlog item.
- **macOS endpoint setup** via launchd. The endpoint install/setup paths are Linux-only; macOS endpoints need manual launchd plists.
- **`docs/reference/` content** — SSH bastion + reverse-tunnel primer, contract docs, Pattern B walk-through. Backlog.
- **CI/CD** — no GitHub Actions yet.
- **Distribution** — no Homebrew tap; install from source only.
- **End-to-end smoke test against a real bastion.** Help output verified; no live deploy of this binary against the bastion yet.

## See also

- [decisions/inherited.md](decisions/inherited.md) — upstream decisions
- [planning/backlog.md](planning/backlog.md) — what's coming next
- [../deploy/bastion/](../deploy/bastion/) — bastion-VPS install artifacts
