# bastionhub

Self-hosted SSH bastion + reverse-tunnel substrate. Pairs with [sshca](https://github.com/roselabs-io/sshca) for cert auth.

**Status:** Pre-release. v0.1.0-dev — core endpoint management commands working; bastion-side setup is documented + scripted but not yet wrapped in CLI.

## What it does

`bastionhub` packages the "self-host a central SSH bastion + a small fleet of devices that hold persistent reverse tunnels back to it" recipe as a single tool, with sane defaults and cert-auth integration. Three audiences:

- **Solo devs** who want to reach home-lab boxes from anywhere without a VPN.
- **Small teams** that want self-hosted "reach our boxes through NAT" without committing to Tailscale.
- **Sovereignty-conscious shops** that won't put a managed-control-plane daemon on prod gear.

## Scope

Substrate only. Three things bastionhub explicitly does NOT do:

- **No CA / cert mechanics.** Shells out to [sshca](https://github.com/roselabs-io/sshca) for signing host and tunnel certs. Bastionhub never holds CA private keys.
- **No multi-tenant policy / registry schema.** No concept of "customer" or "role" — just tunnel endpoints. Upstream tools (e.g. the [gateway](https://github.com/roselabs-io/gateway) OT product) compile their richer registries down to bastionhub's local config.
- **No other substrates.** No Tailscale, WireGuard, or DERP. SSH-bastion + reverse-tunnel only. If those substrates become interesting, they get their own tools.

## Install

From source (until Homebrew tap lands):

```sh
git clone https://github.com/roselabs-io/bastionhub.git
cd bastionhub
go build -o bastionhub .       # Linux, macOS
go build -o bastionhub.exe .   # Windows (PowerShell)
```

Cross-compiled binaries for windows-amd64 / windows-arm64 / linux-amd64 / darwin-arm64 build cleanly from any platform; CI/Homebrew tap distribution is on the backlog.

**Engineer laptop:** Linux, macOS, and Windows all work for the engineer-side commands (`list`, `status`, `ssh`, `endpoint register/enroll/unregister`).

**Endpoint device** (where the reverse tunnel autossh service runs): Linux + macOS are supported via `bastionhub endpoint install/setup`. Windows as an endpoint is out of scope — see [docs/planning/backlog.md](docs/planning/backlog.md) "Parked" for the rationale.

Bastionhub depends on `sshca` being in `PATH` for cert operations:

```sh
git clone https://github.com/roselabs-io/sshca.git
cd sshca
go build -o sshca .
sudo mv sshca /usr/local/bin/
```

## Quick start

```sh
# 0. (one-time) Stand up the bastion VPS. See deploy/bastion/README.md.

# 1. Create the local endpoints registry
bastionhub init

# 2. Enroll an endpoint — one shot: signs the tunnel cert (via sshca) + registers
bastionhub endpoint enroll perso-mbp --pubkey-file ./perso-mbp.pub
# → outputs the assigned port + ships you a cert file to deliver to the endpoint

# 3. On the endpoint device, once the cert is in place:
#    Linux (needs sudo, uses systemd + apt/dnf/yum/apk):
sudo bastionhub endpoint install
sudo bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    macOS (per-user, no sudo, uses launchd + brew):
bastionhub endpoint install
bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    Both support --dry-run on setup for inspection without writing/loading.

# 4. Daily use — from the engineer laptop:
bastionhub list                    # show configured endpoints
bastionhub status                  # query bastion for live tunnels
bastionhub ssh perso-mbp           # ProxyJump via bastion
```

The config lives at `~/.config/bastionhub/endpoints.yaml` (override with `$BASTIONHUB_CONFIG`).

## See also

- [docs/](docs/) — full documentation tree
- [docs/decisions/inherited.md](docs/decisions/inherited.md) — upstream ADRs that motivated this tool
- [deploy/bastion/](deploy/bastion/) — sshd drop-ins + Pattern B `AuthorizedPrincipalsCommand` script for the bastion VPS

## License

MIT. See [LICENSE](LICENSE).
