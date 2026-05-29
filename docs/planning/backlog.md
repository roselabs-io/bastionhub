# Backlog

> Forward-looking only. For "what works right now?", see [../current-state.md](../current-state.md). For "what was decided?", see [../decisions/](../decisions/).

## Active

1. **Ship a complete bastion-side starter sshd config.** Currently `deploy/bastion/` has the Pattern B drop-in and script. Missing: the foundational `Match User gw-tunnel` (with `PermitListen 12001-12099`, `ForceCommand /bin/false`, `PermitTTY no`), `Match User gw-user`, `TrustedUserCAKeys`, `RevokedKeys` blocks. These live as manual setup on Patrick's bastion today; bastionhub should ship them as `deploy/bastion/00-bastionhub.conf` so a fresh deploy is reproducible from `git clone`.
2. **End-to-end test against the real bastion.** Verify the migrated binary works end-to-end on Patrick's existing setup: `bastionhub init`, copy current `gateways.yaml` content to `endpoints.yaml`, `bastionhub list/status`, `bastionhub ssh perso-mbp`, `bastionhub endpoint enroll` for a new endpoint.
3. **Migration guide** for the in-place hand-off from `gwctl` to `bastionhub`. Steps: install `sshca` + `bastionhub`, `cp ~/.config/gwctl/gateways.yaml ~/.config/bastionhub/endpoints.yaml`, edit `gateways:` key → `endpoints:` key, verify with `bastionhub list/status`. One-time, single user, no shim needed.
4. **macOS endpoint setup via launchd.** The endpoint install/setup paths are Linux-only today (assume systemd). macOS endpoints (Patrick's perso-mbp + work-laptop) currently use manual launchd plist setup. Wrap as `bastionhub endpoint install` / `setup` on macOS too.

## Soon

- **First bastionhub-series ADR.** First independent decision — likely the bastion-side config split (00-bastionhub.conf scope) or macOS launchd path.
- **`bastionhub bastion verify`** — quick sanity check the bastion VPS is configured correctly: sshd drop-ins present, `gw-tunnel` user exists, `gw-passthrough` user exists, `TrustedUserCAKeys` pubkey matches local `sshca`'s, KRL is readable.
- **Pattern B registry-driven script.** Today's `principal-to-acl.sh` emits a fixed list. Replace with a script that reads a config file (per-principal `permitopen` mapping) so adding a new edge-passthrough doesn't require editing the script itself.
- **CI/CD setup.** GitHub Actions: build, test, release. Single binary per platform.
- **Homebrew tap.** Shared `roselabs-io/homebrew-tools` with `sshca` and `bastionhub` formulae.

## Later

- **Bastion-side `bastionhub install` command.** Wraps the manual deploy steps (create `gw-tunnel`/`gw-user`/`gw-passthrough` users, install drop-ins, restart sshd) as one command. Risky operation; needs `--dry-run` and good docs first.
- **HA bastion** (multi-VPS). Today's model is single-bastion. Endpoints could dial to multiple bastions for failover. Don't build until someone actually needs it.
- **OpenWrt endpoint** (V1 hardware). The `gateway` product's V1 lands on BPI-R3 / OpenWrt. Endpoint setup there will look different from Linux/systemd (procd, uci). Bastionhub can grow an OpenWrt path.
- **Endpoint heartbeat / health signal.** Today `bastionhub status` queries the bastion for live listeners. An endpoint-side heartbeat (separate from autossh's reconnect) could surface flapping connections.

## Parked

- **Multi-substrate** (Tailscale, WireGuard, DERP). Out of scope per upstream [ADR-008](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-008-extract-bastion-substrate-as-bastionhub.md). bastionhub is SSH-bastion + reverse-tunnel only. Other substrates get their own tools.
- **Registry of customers/projects/roles.** Belongs in the consumer (the `gateway` product), not bastionhub. Endpoints registry is intentionally schema-narrow.
- **Fleet console / observability / deploy gate.** Product features, not substrate. Live upstream in `gateway`.
- **Cert mechanics / CA.** All cert ops shell out to [`sshca`](https://github.com/roselabs-io/sshca). Bastionhub never holds CA keys.
