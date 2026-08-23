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

| | Engineer-side (`list` / `status` / `ssh` / `invite` / `endpoint register/enroll/unregister`) | Endpoint-side (`endpoint install` / `setup`) | Far end via `invite` |
|---|---|---|---|
| Linux | ✓ | ✓ (systemd + apt/dnf/yum/apk) | ✓ (systemd) |
| macOS | ✓ | ✓ (launchd + Homebrew, per-user, no sudo) | ✓ (launchd) |
| Windows | ✓ | ✗ (needs autossh + a service wrapper) | ✓ (scheduled task) |

The last column is the enrollment path added in v0.2. It needs nothing installed
on the far end — no bastionhub, no autossh, no admin rights — because the
bootstrap script uses only `ssh`, `ssh-keygen` and `curl` (or PowerShell's
`iwr`), all of which ship with Windows 10/11, macOS and Linux. Persistence comes
from systemd `Restart=always` / launchd `KeepAlive` / a scheduled task around
plain `ssh`, with `ServerAliveInterval` doing the dead-peer detection autossh
would otherwise provide.

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

## Enrolling a machine you don't control

`bastionhub endpoint install/setup` works when you can get a shell on the device.
When you can't — a controller on a customer's floor, with a technician on the
phone — the problem is delivering a certificate to a machine nobody provisioned.
Doing that by hand means generating a keypair *for* someone else and sending
them a **private key**, which is not something to do over chat.

`bastionhub serve` closes that gap. It runs on the bastion, mints single-use
invite codes, and relays public material between the far end and you:

```sh
# On the bastion (once):
bastionhub serve --bastion bastion.example.io --listen 127.0.0.1:8420
# → prints an admin token on first run. Put it on your laptop.

# On your laptop, where the CA lives:
export BASTIONHUB_SERVE_URL=https://bastion.example.io
export BASTIONHUB_ADMIN_TOKEN=<token>

bastionhub invite tex-mmv2
```

```
Read this to whoever is on site (expires in 30 min):

    curl -sSL https://bastion.example.io/j/3DY2FNYT | sh      # mac / linux
    iwr -useb https://bastion.example.io/j/3DY2FNYT | iex     # windows

Code: 3DY2-FNYT

waiting for the far end… ✓ public key received

  fingerprint: 256 SHA256:LIvVkKqGGYj2Jo8uhwo4IXHOlnc+7MGfZL8pRi+mwoQ (ED25519)
  will sign as: principal=gw-tunnel valid=+52w port=12005

Ask them to read their fingerprint back. Sign it? [y/N] y
✓ certificate signed and sent

✓ tex-mmv2 enrolled on port 12005
```

The far end generates its own keypair and sends only the public half. Ask them
to read the fingerprint back before you answer that prompt — it is the only
thing tying the key you are about to sign to the person you are talking to.

### The service never holds the CA

This is the design constraint, not an implementation detail. `serve` is a
rendezvous point: it stores public keys and certificates and hands them between
two parties who cannot otherwise reach each other. Signing happens on your
laptop, where the CA is, exactly as `endpoint enroll` already does.

If the bastion were fully compromised, the attacker would get a list of public
keys and some expired codes. They could not mint a single certificate.

The cost of that property: **you have to be online when an invite is redeemed.**
That is a direct consequence of the CA not being on the VPS, and it is the
intended trade.

### Three shapes

Two directions of access, and they are not interchangeable — `gw-tunnel` may
listen but gets no shell and no local forwards; `gw-user` may open local
forwards so ProxyJump works, but may not listen.

| `--shape` | For | What it does | Principal | Default validity |
|---|---|---|---|---|
| `device` | A machine that must be **reachable** and stays — controller, gateway box | systemd / launchd / scheduled task around `ssh -R`; survives reboots | `gw-tunnel` | `+52w` |
| `session` | A machine that must be reachable for **one sitting** | holds `ssh -R` in the foreground; closing the window removes the key | `gw-tunnel` | `+12h` |
| `access` | A machine that needs to **reach** the fleet — your other laptop | writes a cert and an ssh config block; opens no tunnel and runs nothing | `gw-user` | `+12h` |

The principal follows the shape automatically. Overriding it is allowed but
warned about, because the mismatch is silent: a `gw-user` cert authenticates
fine and then cannot open the tunnel the script just set up.

### Access fans out

An `access` cert is not bound to any endpoint. It authenticates to the bastion
as `gw-user`, and from there ProxyJump reaches **any** port the bastion is
listening on — including endpoints enrolled long after the cert was issued.

So the second laptop is a one-time setup:

```sh
bastionhub invite my-work-laptop --shape access --valid +52w
```

Every device you invite afterwards is reachable from it with no re-issue. The
cert's validity window is the only thing that ends it — which is what `sshca
cert revoke` and the KRL are for if it needs to end sooner.

### Serving it

`serve` binds `127.0.0.1:8420` by default and expects a TLS-terminating proxy in
front, since the invite line tells a stranger to pipe a URL into a shell. Pass
`--tls-cert`/`--tls-key` to serve HTTPS directly instead.

## Stability promises

Pre-1.0: minor releases may break things. Breaking changes will be called out in [CHANGELOG.md](CHANGELOG.md) with the rationale.

Post-1.0: [SemVer](https://semver.org/). Three surfaces are versioned:

- **CLI grammar** — subcommand names, flag names, exit codes
- **`endpoints.yaml` schema** — fields documented in [CLAUDE.md](CLAUDE.md) "Contract surface"
- **Deploy artifact identifiers** — `bastionhub-tunnel.service` (systemd unit), `com.roselabs.bastionhub-tunnel` (launchd label), `10-bastionhub.conf` / `30-passthrough-acl.conf` (sshd drop-ins)
- **`serve` HTTP surface** — the far-end routes (`/j/<code>`, `/e/<code>/pubkey`, `/e/<code>/cert`) are the contract a bootstrap script in the wild depends on. The `/api/` routes are operator-facing and move with the CLI.

## Roadmap

- **v0.2** — `bastionhub serve` + `bastionhub invite` (above). Homebrew tap live.
- **Next** — a self-host install script: fresh VPS to working bastion in one command, including `serve`, the sshd drop-ins, the role users, and TLS.
- **Soon** — `bastionhub bastion verify` (sanity-check a bastion VPS's sshd drop-ins, role users, KRL).
- **Later** — registry-driven Pattern B (the current `principal-to-acl.sh` emits a fixed list; future versions read a config file). OpenWrt endpoint support.

## See also

- [sshca](https://github.com/roselabs-io/sshca) — cert tool bastionhub depends on
- [deploy/bastion/](deploy/bastion/) — sshd drop-ins + Pattern B `AuthorizedPrincipalsCommand` script for the bastion VPS

## License

MIT. See [LICENSE](LICENSE).
