# bastionhub

Self-hosted SSH bastion + reverse-tunnel substrate. Pairs with [sshca](https://github.com/roselabs-io/sshca) for cert auth.

**v0.1.0** — initial release. Single Go binary, two deps (`urfave/cli/v3` + `yaml.v3`). Linux + macOS + Windows.

## What it does

`bastionhub` packages the "self-host a central SSH bastion + a small fleet of devices that hold persistent reverse tunnels back to it" recipe as a single tool, with sane defaults and cert-auth integration. Three audiences:

- **Solo devs** who want to reach home-lab boxes from anywhere without a VPN.
- **Small teams** that want self-hosted "reach our boxes through NAT" without committing to Tailscale.
- **Sovereignty-conscious shops** that won't put a managed-control-plane daemon on prod gear.

## Scope

Substrate only. Three things bastionhub explicitly does NOT do:

- **No CA / cert mechanics.** Shells out to [sshca](https://github.com/roselabs-io/sshca) for signing host and tunnel certs. Bastionhub never holds CA private keys.
- **No multi-tenant policy / registry schema.** No concept of "customer" or "role" — just tunnel endpoints. Upstream tools (e.g. an OT-integrator product layer) compile their richer registries down to bastionhub's local config.
- **No other substrates.** No Tailscale, WireGuard, or DERP. SSH-bastion + reverse-tunnel only.

## Install

### Homebrew (macOS + Linuxbrew)

```sh
brew tap roselabs-io/tools
brew install bastionhub
brew install sshca       # required runtime dependency
```

### From source

```sh
git clone https://github.com/roselabs-io/bastionhub.git
cd bastionhub
go build -o bastionhub .       # Linux, macOS
go build -o bastionhub.exe .   # Windows
```

Plus `sshca` from [github.com/roselabs-io/sshca](https://github.com/roselabs-io/sshca) installed somewhere on `PATH`.

### Pre-built binaries

Download from [GitHub Releases](https://github.com/roselabs-io/bastionhub/releases) — `.tar.gz` for Unix, `.zip` for Windows, six platforms (linux/darwin/windows × amd64/arm64), SHA-256 checksums attached.

## Platform support

| | Engineer-side (`list` / `status` / `ssh` / `endpoint register/enroll/unregister`) | Endpoint-side (`endpoint install` / `setup`) |
|---|---|---|
| Linux | ✓ | ✓ (systemd + apt/dnf/yum/apk) |
| macOS | ✓ | ✓ (launchd + Homebrew, per-user, no sudo) |
| Windows | ✓ | ✗ (out of scope — Windows boxes are typically reached *behind* an endpoint, not as one) |

## Quick start

```sh
# 0. (one-time) Stand up the bastion VPS. See deploy/bastion/README.md.

# 1. Create the local endpoints registry
bastionhub init

# 2. Enroll an endpoint — one shot: signs the tunnel cert (via sshca) + registers
bastionhub endpoint enroll my-endpoint --pubkey-file ./my-endpoint.pub
# → outputs the assigned port + ships you a cert file to deliver to the endpoint

# 3. On the endpoint device, once the cert is in place:
#    Linux (needs sudo, systemd + apt/dnf/yum/apk):
sudo bastionhub endpoint install
sudo bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    macOS (per-user, no sudo, launchd + brew):
bastionhub endpoint install
bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    Both support --dry-run on setup for inspection without writing/loading.

# 4. Daily use — from the engineer laptop:
bastionhub list                       # show configured endpoints
bastionhub status                     # query bastion for live tunnels
bastionhub ssh my-endpoint            # ProxyJump via bastion
bastionhub ssh my-endpoint -- uptime  # one-off remote command
```

The config lives at `~/.config/bastionhub/endpoints.yaml` (override with `$BASTIONHUB_CONFIG`).

## Stability promises

Pre-1.0: minor releases may break things. Breaking changes will be called out in [CHANGELOG.md](CHANGELOG.md) with the rationale.

Post-1.0: [SemVer](https://semver.org/). Three surfaces are versioned:

- **CLI grammar** — subcommand names, flag names, exit codes
- **`endpoints.yaml` schema** — fields documented in [CLAUDE.md](CLAUDE.md) "Contract surface"
- **Deploy artifact identifiers** — `bastionhub-tunnel.service` (systemd unit), `com.roselabs.bastionhub-tunnel` (launchd label), `10-bastionhub.conf` / `30-passthrough-acl.conf` (sshd drop-ins)

## Roadmap

- **v0.2** — `roselabs-io/homebrew-tools` tap; tagged release pipeline polish.
- **Soon** — `bastionhub bastion verify` (sanity-check a bastion VPS's sshd drop-ins, role users, KRL).
- **Later** — registry-driven Pattern B (the current `principal-to-acl.sh` emits a fixed list; future versions read a config file). OpenWrt endpoint support.

## See also

- [sshca](https://github.com/roselabs-io/sshca) — cert tool bastionhub depends on
- [deploy/bastion/](deploy/bastion/) — sshd drop-ins + Pattern B `AuthorizedPrincipalsCommand` script for the bastion VPS

## License

MIT. See [LICENSE](LICENSE).
