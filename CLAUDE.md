# bastionhub — contributor / agent context

Self-hosted SSH bastion + reverse-tunnel substrate. Pairs with [sshca](https://github.com/roselabs-io/sshca) for cert auth. Single Go binary, two intended deps (`urfave/cli/v3` v3.9.0 + `gopkg.in/yaml.v3` v3.0.1). Substrate-narrow scope: no policy, no registry, no observability.

## Read order

1. This file
2. [README.md](README.md) — install + Quick start + Stability promises
3. [CHANGELOG.md](CHANGELOG.md) — what shipped per release
4. [main.go](main.go) + [main_test.go](main_test.go) — engineer-side + dispatch + Linux/Darwin install/setup
5. [deploy/bastion/](deploy/bastion/) — bastion-VPS sshd drop-ins + Pattern B script

## Project shape

**What this is:** a small CLI + deploy scripts for running a self-hosted SSH bastion with reverse tunnels from a fleet of "endpoints" (boxes behind NAT that dial home to the bastion). Cert auth via `sshca`. Engineer-side commands cross-platform; endpoint-side `install`/`setup` Linux + macOS.

**What this isn't:**

- A cert authority. Shells out to `sshca` for every cert operation.
- A policy engine. No roles, no customers, no projects. Just tunnel endpoints.
- A multi-substrate connectivity tool. SSH-bastion + reverse-tunnel only.

## Key principles

1. **Substrate-narrow scope.** Run the SSH-bastion + reverse-tunnel pattern well. Anything richer (policy, audit beyond connection logs, fleet observability) belongs upstream.
2. **Cert auth via `sshca`.** No in-process signing. `sshca` is a required runtime dependency (`bastionhub endpoint enroll` shells out to it).
3. **Schema-neutral local config.** `endpoints.yaml` carries port, user, identity, description. It does NOT know about customer, role, or principal vocabulary.
4. **Stock OpenSSH on both ends.** No custom sshd, no patches. Drop-ins + `AuthorizedPrincipalsCommand` for per-principal scoping (Pattern B).
5. **Reverse tunnels via autossh + systemd / launchd.** Battle-tested, not invented here.

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
