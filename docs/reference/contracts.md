# Contract surface

This document declares the surface area downstream consumers depend on. **Breaking changes here require a major version bump and a deprecation cycle.** The discipline holds even when the only consumer is a sibling tool in the same `roselabs-io/` org (today: the [`gateway`](https://github.com/roselabs-io/gateway) product).

Three surfaces are versioned:

1. **CLI grammar** — subcommand names, flag names, argument positions, exit codes
2. **`endpoints.yaml` schema** — field names, types, defaults, what consumers can rely on
3. **Deploy artifact paths** — where the systemd unit / launchd plist / sshd drop-ins land, what they're named

Not versioned: error message wording, help text wording, log lines emitted to stderr, the specific autossh / ssh options used internally (those will get tuned over time).

bastionhub also depends on [`sshca`](https://github.com/roselabs-io/sshca) as a subprocess. The integration contract is bidirectional: bastionhub pins itself to sshca's contracts (CLI grammar); sshca pins itself to its own grammar. Cross-tool breaking changes require coordinated major versions.

---

## 1. CLI grammar

### Stability levels

| Level | Meaning |
|---|---|
| **Stable** | Will not change name / position / type without a major version bump + 1 minor of deprecation warning. |
| **Provisional** | May change in a minor version. Used during pre-1.0 development; consumers should pin to a specific minor or wait for stabilization. |

All of v0.x is **provisional** until v1.0. Treat the table below as the *intended* stable shape.

### Subcommand tree

```
bastionhub
├── init       [stable]   Create starter ~/.config/bastionhub/endpoints.yaml
├── list       [stable]   List configured endpoints
├── status     [stable]   Query bastion for tunnel liveness
├── ssh        [stable]   ProxyJump to an endpoint
└── endpoint
    ├── register    [stable]    Add to endpoints.yaml (no cert work)
    ├── unregister  [stable]    Remove from endpoints.yaml
    ├── enroll      [stable]    register + shell out to `sshca cert sign`
    ├── install     [stable]    [endpoint-side] autossh + key generation
    └── setup       [stable]    [endpoint-side] write service unit/plist + start
```

### Global flags

| Flag | Type | Default | Notes |
|---|---|---|---|
| `--version` / `-v` | bool | false | Print version, exit 0. |
| `--help` / `-h` | bool | false | Print help, exit 0. |

### Per-command flags

#### `bastionhub init`

No flags. Writes a starter `~/.config/bastionhub/endpoints.yaml` (or `$BASTIONHUB_CONFIG`). Refuses to overwrite. Exit codes: `0` success, `1` already exists / write failed.

#### `bastionhub list`

No flags. Tabular output: NAME, PORT, USER, DESCRIPTION columns. Header line above. Exit codes: `0`, `1` if config can't be read.

#### `bastionhub status`

No flags. SSH to `admin_alias` to inspect live listeners. Tabular output: NAME, PORT, STATUS, UPTIME, DESCRIPTION. Exit codes: `0` (whether or not endpoints are UP), `1` if SSH to admin alias fails.

#### `bastionhub ssh`

Positional: `<endpoint-name>` then arbitrary additional args passed to `ssh`.

Behavior:
- Resolves endpoint from `endpoints.yaml` → constructs `ssh -J <bastion_alias> <user>@127.0.0.1 -p <port> [extra args...]`
- If endpoint has `identity:` field, adds `-i <identity> -o IdentitiesOnly=yes`
- Spawns `ssh` with inherited stdio. Cross-platform (Linux, macOS, Windows).

Exit codes: propagated from ssh (e.g. `255` for connection failure, `0` for successful session, custom remote exit codes when ssh runs a remote command).

#### `bastionhub endpoint register`

```
bastionhub endpoint register <name> [--user <U>] [--port <N>] [--identity <path>] [--description <text>]
```

| Flag | Type | Default | Notes |
|---|---|---|---|
| `--user` | string | `root` | Unix user on the endpoint device. |
| `--port` | int | next free in `12001-12099` | Bastion-side listen port. |
| `--identity` | string | empty | SSH key to use for laptop→endpoint hop (avoids MaxAuthTries blow-out). |
| `--description` / `-d` | string | empty | Free-form. |

Exit codes: `0`, `1` (name exists, port collision, write failure).

#### `bastionhub endpoint unregister`

Positional: `<name>`. Removes entry from `endpoints.yaml`. Does NOT revoke certs (use `sshca cert revoke` separately). Exit codes: `0`, `1` (not found, write failure).

#### `bastionhub endpoint enroll`

```
bastionhub endpoint enroll <name> --pubkey-file <path> [...]
```

| Flag | Type | Required | Default | Notes |
|---|---|---|---|---|
| `--pubkey-file` | string | yes | — | Endpoint's outbound SSH pubkey. |
| `--user` | string | no | `root` | |
| `--port` | int | no | next free | |
| `--identity` | string | no | empty | |
| `--description` / `-d` | string | no | empty | |
| `--valid` | string | no | `+52w` | Passed through to sshca. |
| `--principal` | string | no | `gw-tunnel` | Passed through to sshca. |
| `--ca-dir` | string | no | empty | Passed to `sshca --dir` if set. |

Behavior:
1. Validates pubkey file + sshca in PATH.
2. Shells out to: `sshca cert sign --ca user --principal <P> --valid <V> --key-id <auto> [--dir <D>] <pubkey>`.
3. Auto-generated key-id: `<name>-tunnel-<UTC-timestamp>` (timestamp format `YYYYMMDDTHHmmZ`).
4. Verifies the produced `<pubkey>-cert.pub` exists.
5. Adds the endpoint to `endpoints.yaml`.
6. Prints next-steps message.

Exit codes: `0`, `1` (sshca failure, registry write failure, validation failure).

#### `bastionhub endpoint install`

No flags. Dispatches by `runtime.GOOS`. Exit codes: `0`, `1` (unsupported OS, package manager failure, ssh-keygen failure).

Per-OS behavior:

| OS | Privilege | Package mgr | Key location |
|---|---|---|---|
| Linux | root (sudo) | apt-get / dnf / yum / apk (first available) | `/etc/bastionhub-tunnel/id_ed25519` |
| macOS | user (NOT sudo) | Homebrew (`brew install autossh`) | `~/.bastionhub-tunnel/id_ed25519` |
| other | — | (unsupported, exits 1) | — |

#### `bastionhub endpoint setup`

| Flag | Type | Required | Default | Notes |
|---|---|---|---|---|
| `--port` | int | yes | — | Bastion-side listen port (must match enroll/register). |
| `--bastion` | string | yes | — | Public hostname/IP of bastion. |
| `--bastion-user` | string | no | `gw-tunnel` | Unix user on bastion to authenticate as. |
| `--dry-run` | bool | no | false | Print the unit/plist + commands without writing or loading. |

Per-OS behavior:

| OS | Service mgr | Service location | Service identifier |
|---|---|---|---|
| Linux | systemd | `/etc/systemd/system/bastionhub-tunnel.service` | `bastionhub-tunnel.service` |
| macOS | launchd | `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist` | `com.roselabs.bastionhub-tunnel` |
| other | — | (unsupported, exits 1) | — |

Exit codes: `0`, `1` (validation, write failure, service manager failure).

### Environment variables

| Variable | Purpose |
|---|---|
| `BASTIONHUB_CONFIG` | Override default `~/.config/bastionhub/endpoints.yaml` path. |

bastionhub does NOT consume `SSHCA_CA_DIR` directly — it's passed through to `sshca` via subprocess inheritance. `bastionhub endpoint enroll --ca-dir <D>` passes `--dir <D>` to sshca; absent that flag, sshca uses its own default resolution (its `--dir` default → `$SSHCA_CA_DIR` → `./ca`).

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Generic error (validation, file not found, subprocess failure). |
| (other) | For `bastionhub ssh`: propagated from `ssh` directly (e.g. `255` for connection failure). |

---

## 2. `endpoints.yaml` schema

Location: `~/.config/bastionhub/endpoints.yaml` (or `$BASTIONHUB_CONFIG`). The gateway product's registry compiles down to this file; bastionhub reads it.

### Schema (v0.x — stable)

```yaml
# Top-level: scalars and the endpoints map
bastion_alias: <string>      # SSH host alias for ProxyJump (default: "bastion")
admin_alias:   <string>      # SSH host alias for admin/status (default: "bastion-root")

endpoints:                   # map of endpoint name → Endpoint
  <name>:
    port:        <int>       # bastion-side listen port (required; 12001-12099 by convention)
    user:        <string>    # Unix user on the endpoint (required)
    identity:    <string>    # path to SSH key for laptop→endpoint hop (optional)
    description: <string>    # free-form text (optional)
```

### Field details

| Field | Path | Type | Required | Stability |
|---|---|---|---|---|
| `bastion_alias` | top-level | string | no (default `"bastion"`) | Stable. |
| `admin_alias` | top-level | string | no (default `"bastion-root"`) | Stable. |
| `endpoints` | top-level | map | no (defaults to empty map; an absent or YAML-`null`-valued `endpoints:` key is treated as empty) | Stable. |
| `port` | per-endpoint | int | yes | Stable. Conventional range `12001-12099`; bastionhub `register` allocates from this range. |
| `user` | per-endpoint | string | yes (defaults to `"root"` if absent in YAML but written by register if not supplied as flag) | Stable. |
| `identity` | per-endpoint | string | no | Stable. When set, `bastionhub ssh` adds `-i <identity> -o IdentitiesOnly=yes` to avoid MaxAuthTries blow-out. |
| `description` | per-endpoint | string | no | Stable. Human-readable. |

### Compatibility notes

- Unknown top-level keys: bastionhub will ignore them. Consumers may safely include their own annotations.
- Unknown per-endpoint keys: ignored. Same for consumer annotations.
- YAML 1.2; `gopkg.in/yaml.v3` is the parser. Comments are preserved if written by bastionhub commands but not exposed programmatically.
- File permissions: bastionhub writes with mode `0o600`. Operators who hand-edit should preserve this.
- New fields may be added in minor versions. Existing fields will not be renamed, retyped, or removed without a major version bump.

### Compilation contract (for upstream consumers)

The gateway product (or any upstream registry tool) compiles its richer schema down to this file. The compilation is unidirectional: gateway → bastionhub. bastionhub does not read gateway's schema and does not write back upstream.

A compilation tool should:

1. Generate the `endpoints` map from its registry, one entry per gateway-product "endpoint" that should have a tunnel.
2. Preserve `bastion_alias` + `admin_alias` (probably constants per-deployment).
3. Write atomically (write to `endpoints.yaml.tmp`, rename to `endpoints.yaml`) so bastionhub never observes a partial file.
4. Set file mode `0o600`.
5. Not include `description` fields that mention OT-product internals (customer names, etc.) if the file is shipped beyond the operator's machine.

---

## 3. Deploy artifact paths

### Endpoint-side (where the autossh service runs)

| Path | OS | Purpose | Stability |
|---|---|---|---|
| `/etc/bastionhub-tunnel/id_ed25519` | Linux | tunnel private key | Stable |
| `/etc/bastionhub-tunnel/id_ed25519.pub` | Linux | tunnel pubkey | Stable |
| `/etc/bastionhub-tunnel/id_ed25519-cert.pub` | Linux | tunnel cert (signed by sshca) | Stable |
| `/etc/bastionhub-tunnel/known_hosts` | Linux | per-tunnel known_hosts | Stable |
| `/etc/systemd/system/bastionhub-tunnel.service` | Linux | systemd unit | Stable |
| `/var/log/bastionhub-tunnel-autossh.log` | Linux | autossh log | Stable |
| `~/.bastionhub-tunnel/id_ed25519` | macOS | tunnel private key | Stable |
| `~/.bastionhub-tunnel/id_ed25519.pub` | macOS | tunnel pubkey | Stable |
| `~/.bastionhub-tunnel/id_ed25519-cert.pub` | macOS | tunnel cert | Stable |
| `~/.bastionhub-tunnel/known_hosts` | macOS | per-tunnel known_hosts | Stable |
| `~/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist` | macOS | launchd plist | Stable |
| `~/Library/Logs/bastionhub/{stdout,stderr,autossh}.log` | macOS | service + autossh logs | Stable |

The plist label `com.roselabs.bastionhub-tunnel` and the systemd unit name `bastionhub-tunnel.service` are part of the contract — external monitoring tools may key on these identifiers.

### Bastion-side (where sshd verifies)

Shipped via [`deploy/bastion/`](../../deploy/bastion/) — install via the procedure in that directory's README. The files (`10-bastionhub.conf`, `30-passthrough-acl.conf`, `principal-to-acl.sh`) deploy to `/etc/ssh/sshd_config.d/` and `/usr/local/bin/`. Stability: filenames, deploy targets, and required permissions are stable.

Bastion-side artifacts depend on `sshca`'s CA dir layout (specifically `user_ca.pub` being shipped to `/etc/ssh/user_ca.pub`). That binding is part of the sshca contract — see [sshca docs/reference/contracts.md §3](https://github.com/roselabs-io/sshca/blob/main/docs/reference/contracts.md#3-ca-directory-layout).

---

## 4. Versioning policy

[Semantic Versioning](https://semver.org/) for all contracts above.

- **Patch** (`v0.1.0` → `v0.1.1`): bug fixes, internal refactors. No contract changes.
- **Minor** (`v0.1.x` → `v0.2.0`): additive — new flags with defaults, new subcommands, new optional `endpoints.yaml` fields. Existing contracts unchanged.
- **Major** (`v0.x` → `v1.0`, eventually `v1` → `v2`): breaking changes.

**v0.x specifically**: stability is *intended* (the surfaces above are stable in design) but not *guaranteed* (renames are still possible during pre-1.0 hardening). v1.0 is the cutoff where the discipline becomes contractual.

Cross-tool versioning: bastionhub's major version is independent of sshca's, but the two move in coordinated lockstep at major version boundaries — e.g., bastionhub v2.0 + sshca v2.0 may ship together with coordinated breaking changes to the subprocess invocation contract.

---

## 5. References

- [README.md](../../README.md) — public-facing pitch + Quick Start
- [docs/current-state.md](../current-state.md) — what works right now
- [deploy/bastion/README.md](../../deploy/bastion/README.md) — bastion-side install runbook
- [sshca docs/reference/contracts.md](https://github.com/roselabs-io/sshca/blob/main/docs/reference/contracts.md) — sister contract surface
- [decisions/inherited.md](../decisions/inherited.md) — upstream ADRs that informed bastionhub's existence
