# Inherited decisions

`bastionhub` was extracted from the `roselabs-io/gateway` monorepo during the three-repo decomposition (2026-05-29). The decisions that motivated bastionhub's existence and shape are documented as ADRs in the `gateway` repo's `docs/decisions/`. They're **not duplicated here** — links below, with bastionhub-specific relevance noted.

This repo's own ADR series starts fresh at ADR-001 when bastionhub makes its first independent decision (see [README.md](README.md)).

## ADRs from `gateway` that apply to `bastionhub`

| Upstream ADR | Subject | Relevance to bastionhub |
|---|---|---|
| ADR-001 | Replace raw-key auth with SSH certificates | Cert auth is bastionhub's assumed authentication model. Raw-key auth was the V0 approach; cert auth is V0.6+. |
| ADR-004 | Principal taxonomy — default principals grant no shell | The substrate-level enforcement bastionhub ships: `Match User gw-tunnel` (etc.) sshd drop-ins, Pattern B `AuthorizedPrincipalsCommand` for per-principal scoping. Bastionhub doesn't define the principal vocabulary; it enforces it at the sshd layer. |
| ADR-005 | Per-gateway `identity` field | The `identity:` field on `Endpoint` (in `endpoints.yaml`) — passes `-i` + `IdentitiesOnly=yes` to avoid MaxAuthTries blow-out. Renamed conceptually (gateway → endpoint) but the mechanism is identical. |
| ADR-006 | Bifurcate `sshca` from gateway product | Why bastionhub depends on `sshca` instead of bundling cert mechanics. `bastionhub endpoint enroll` shells out to `sshca cert sign`. |
| ADR-007 | Retire `gwctl`; three-repo decomposition | The overall context: `sshca` + `bastionhub` + `gateway` as three independent tools, hard cut, no shim. |
| ADR-008 | Extract bastion substrate as `bastionhub` | The constitutional ADR for this repo. Scope, registry-separation pattern, what does and doesn't belong here. |

## How to evolve from here

When bastionhub makes a decision that diverges from or extends one of the inherited ADRs (e.g., the bastion-side starter sshd config when that lands, or macOS launchd support), write a fresh ADR in `docs/decisions/` here — don't edit the upstream one. Link back to the inherited ADR being extended.

When an upstream ADR becomes obsolete for bastionhub, note it here with a strikethrough + brief explanation, and write a superseding ADR in this repo.
