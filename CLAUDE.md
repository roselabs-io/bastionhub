# bastionhub — contributor / agent context

SSH bastion and reverse-tunnel manager. Uses [sshca](https://github.com/roselabs-io/sshca) for certificate operations. Single Go binary, two dependencies (`urfave/cli/v3` v3.9.0 and `gopkg.in/yaml.v3` v3.0.1). No policy schema, no observability.

## Read order

1. This file
2. [README.md](README.md) — commands, usage, stability
3. [CHANGELOG.md](CHANGELOG.md) — what shipped per release
4. [main.go](main.go) + [main_test.go](main_test.go) — engineer-side + dispatch + Linux/Darwin install/setup
5. [deploy/](deploy/) — the installer and the bastion-side sshd drop-ins

## Project shape

**What this is:** a CLI and deploy scripts for an SSH bastion with reverse tunnels from endpoints that have no reachable address. Certificate operations are delegated to `sshca`. Operator-side commands run on Linux, macOS and Windows; `endpoint install` and `endpoint setup` run on Linux and macOS.

**What this isn't:**

- A certificate authority. Every certificate operation is delegated to `sshca`.
- A policy engine. No roles, no customers, no projects. Just tunnel endpoints.
- A multi-transport connectivity tool. SSH bastion and reverse tunnels only.

## Key principles

1. **Narrow scope.** SSH bastion and reverse tunnels only. Policy, fleet observability and audit beyond connection logs belong in a caller.
2. **No in-process signing.** `sshca` is a runtime dependency; `endpoint enroll` and `invite` shell out to it.
3. **Schema-neutral config.** `endpoints.yaml` carries port, user, identity and description. It has no concept of customer, role or principal vocabulary.
4. **Stock OpenSSH.** No patched sshd. Configuration is drop-ins, plus `AuthorizedPrincipalsCommand` where per-principal scoping is required.
5. **Persistence via the host init system.** systemd, launchd, or the Windows task scheduler, with autossh where it is available.

## Contract surface (semver-disciplined)

Two things downstream tools depend on. Breaking changes to either require a major version bump.

**CLI grammar:** subcommand names, flag names, argument positions, exit codes. `bastionhub --help` is canonical.

**`endpoints.yaml` schema** at `~/.config/bastionhub/endpoints.yaml` (or `$BASTIONHUB_CONFIG`):

```yaml
bastion_alias: <string>      # default "bastion"; ProxyJump target
admin_alias:   <string>      # default "bastion-root"; for status/admin queries

endpoints:
  <name>:
    port:        <int>       # required; 12001-12099 by convention
    user:        <string>    # required
    identity:    <string>    # optional; path to SSH key for laptop→endpoint hop
    description: <string>    # optional
```

Unknown keys are tolerated (new fields may appear in minor releases). File mode is `0o600`.

**Deploy artifact identifiers** (external monitoring may key on these):

- systemd unit: `bastionhub-tunnel.service`
- launchd plist label: `com.roselabs.bastionhub-tunnel`
- sshd drop-in filenames: `10-bastionhub.conf`, `30-passthrough-acl.conf`

## Don't re-walk these

- **Don't use `Match Principal` in sshd_config.** Not valid OpenSSH syntax — `Match` only accepts `User`, `Group`, `Host`, `LocalAddress`, `LocalPort`, `RDomain`, `Address`. Use `Match User <role>` blocks; the cert's principal list matches the target Unix username by default.
- **Don't add YAML frontmatter to Markdown docs.** Filename + path is enough.
- **Don't add registry / policy concepts to `endpoints.yaml`.** No `customer`, no `role`. Those belong in the consumer that produces this file.
- **Don't bring back `syscall.Exec` in `sshCmd`.** It's Unix-only and breaks Windows. The current `exec.Command + Run()` pattern with inherited stdio is the cross-platform-correct shape.
- **`loadConfig` must re-init the `Endpoints` map post-Unmarshal.** YAML with `endpoints:` having only commented children unmarshals to a nil map; without re-init, the first register panics. Covered by `TestLoadConfig_NilMapRegression`.

## File structure

```
bastionhub/
├── README.md           # public face: install, Quick start, Stability promises
├── CHANGELOG.md        # per-release
├── CLAUDE.md           # this file
├── LICENSE             # MIT
├── main.go             # engineer-side commands + Linux/Darwin install/setup
├── main_test.go        # unit + regression tests
├── go.mod / go.sum
├── .github/workflows/  # CI + release
└── deploy/bastion/     # sshd drop-ins shipped to the bastion VPS
    ├── 10-bastionhub.conf
    ├── 30-passthrough-acl.conf
    ├── principal-to-acl.sh
    └── README.md
```

## Conventions

- **Filenames:** kebab-case
- **Dates:** ISO `YYYY-MM-DD`
- **No YAML frontmatter** on Markdown docs
- **Version variable** in `main.go` is `var` not `const` so release builds inject the tag via `-ldflags "-X main.version=<tag>"`

## See also

- [sshca](https://github.com/roselabs-io/sshca) — cert tool bastionhub depends on. `endpoint enroll` shells out to `sshca cert sign`.
- Internal design notes, roadmap, current operational state, and inherited design rationale live in a private workspace (not public).
