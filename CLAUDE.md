# bastionhub — Project Context

Self-hosted SSH bastion + reverse-tunnel substrate. Pairs with [sshca](https://github.com/roselabs-io/sshca) for cert auth. Single Go binary, two intended deps (`urfave/cli/v3` + `gopkg.in/yaml.v3`). Substrate-narrow scope; no policy, no registry, no observability.

**Status:** Pre-release; v0.1.0-dev. Code migrated from the upstream `gateway` repo per [docs/decisions/inherited.md](docs/decisions/inherited.md).

## Read order for a fresh agent session

1. This file
2. [docs/current-state.md](docs/current-state.md) — what works right now
3. [docs/decisions/](docs/decisions/) — bastionhub's own ADRs + [inherited.md](docs/decisions/inherited.md) for upstream decisions
4. [docs/planning/backlog.md](docs/planning/backlog.md) — open work
5. [deploy/bastion/](deploy/bastion/) — sshd drop-ins for the bastion VPS

## Project shape

**What this is:** a small CLI + deploy scripts for running a self-hosted SSH bastion with reverse tunnels from a fleet of "endpoints" (boxes behind NAT that dial home to the bastion). Cert auth via [sshca](https://github.com/roselabs-io/sshca). Endpoint OS: Linux for the auto-install path; macOS endpoints work but need manual launchd setup (TODO).

**What this isn't:**

- A cert authority — shells out to `sshca` for everything cert-related. Upstream ADR-006, upstream ADR-008.
- A policy engine — no roles, no customers, no projects. Just tunnel endpoints. Schema-rich registries live one layer up.
- A multi-substrate connectivity tool — SSH-bastion + reverse-tunnel only. No Tailscale, no WireGuard.

## Key principles

1. **Substrate-narrow scope.** Run the SSH-bastion + reverse-tunnel pattern well, with sane defaults. Anything richer (policy, audit beyond connection logs, fleet observability) belongs upstream.
2. **Cert auth via `sshca`.** No in-process signing. `sshca` is a required runtime dependency; subprocess invocation.
3. **Schema-neutral local config.** `endpoints.yaml` knows about port, user, identity, description. It does NOT know about customer, role, or principal vocabulary. Upstream tools compile their richer registries down to this.
4. **Stock OpenSSH on both ends.** No custom sshd, no patches. Drop-ins + `AuthorizedPrincipalsCommand` for per-principal scoping (Pattern B; see upstream ADR-004).
5. **Reverse tunnels via autossh + systemd / launchd.** Battle-tested, not invented here.

## Contract surface (semver discipline)

bastionhub exposes two contracts that downstream consumers may depend on:

- **CLI grammar** — subcommands (`ssh`, `list`, `status`, `endpoint *`), flags, exit codes.
- **`endpoints.yaml` schema** — the local-config format that upstream tools (e.g. the `gateway` product) compile down to.

Breaking changes to either require a major version bump and a deprecation cycle. Details land in `docs/reference/contracts.md` as the surface stabilizes.

## Doc maintenance

Same trigger-action discipline as the upstream `gateway` repo:

- **Runtime change** → update [docs/current-state.md](docs/current-state.md)
- **CLI change** → update [README.md](README.md) examples
- **Non-obvious decision** → write an ADR in [docs/decisions/](docs/decisions/)
- **Backlog item ships** → strip from [docs/planning/backlog.md](docs/planning/backlog.md); add one-liner to current-state.md "Recently landed"

## Conventions

- **Filenames:** kebab-case. ADRs: `ADR-NNN-short-description.md` (3-digit zero-padded, monotonic; bastionhub's own series starts at 001).
- **Dates:** ISO `YYYY-MM-DD`.
- **Cross-references:** relative Markdown links inside this repo; absolute GitHub URLs for upstream `gateway` ADRs (see [docs/decisions/inherited.md](docs/decisions/inherited.md)).
- **No frontmatter** on Markdown docs.

## See also

- [README.md](README.md) — public overview
- [docs/decisions/inherited.md](docs/decisions/inherited.md) — upstream ADRs
- [github.com/roselabs-io/sshca](https://github.com/roselabs-io/sshca) — cert tool bastionhub depends on
- The OT-integrator product layer that consumes bastionhub (internal — not OSS).
