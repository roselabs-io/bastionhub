# Backlog

> Forward-looking only. For "what works right now?", see [../current-state.md](../current-state.md). For "what was decided?", see [../decisions/](../decisions/).

## Active

1. **macOS endpoint setup via launchd.** The endpoint install/setup paths are Linux-systemd-only today. Both of Patrick's laptops are macOS managed by hand-written `~/Library/LaunchAgents/*.plist` files. Wrap as `bastionhub endpoint install` / `setup` on Darwin too — generates a launchd plist instead of a systemd unit, per-user (no sudo), brew-detected autossh, plist label per-endpoint (`com.roselabs.bastionhub-tunnel.<name>`). ~4 h.
2. **Cross-platform engineer-laptop support (Windows).** Engineer-side commands (`bastionhub list/status/ssh/endpoint enroll`) should run on Windows since Go cross-compiles trivially and Windows ships OpenSSH. Blocker is `syscall.Exec` in `sshCmd` (Unix-only) — replace with `os/exec` + `cmd.Run()` pattern. Verify on a Windows box / VM, document Windows install steps in README. ~3-4 h. Does NOT include endpoint-side support on Windows (NSSM + autossh-on-Windows is a different beast — parked, see below).

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
- **Windows as a reverse-tunnel endpoint.** The architectural answer is "Windows is a downstream device behind a V1 gateway, not an endpoint itself" — the V1 BPI-R3 jumpbox bridges to whatever Windows gear is on the customer LAN (SCADA, HMI, etc.) via `permitopen`. Supporting Windows-as-endpoint would mean NSSM + autossh-on-Windows quirks for a use case the product architecture already routes around. Revisit only if a credible OT integrator says they specifically need this AND the V1 hardware route doesn't fit their site.
