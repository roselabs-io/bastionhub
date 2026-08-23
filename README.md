# bastionhub

An SSH bastion and reverse-tunnel manager. Single Go binary, two dependencies
(`urfave/cli/v3` and `yaml.v3`). Linux, macOS and Windows. Uses
[sshca](https://github.com/roselabs-io/sshca) for certificate operations.

## Overview

A machine without a reachable address opens an outbound SSH connection to a
bastion and holds it open, binding a port there. Connecting to that port reaches
the machine. `bastionhub` configures both ends of that arrangement: the
bastion's sshd, the endpoint's persistent tunnel, and the certificates that
authenticate them.

It wraps OpenSSH. Signing is delegated to `sshca`; persistence uses systemd,
launchd or the Windows task scheduler; the bastion side is sshd configuration.

The registry of endpoints is a local YAML file at
`~/.config/bastionhub/endpoints.yaml` (`$BASTIONHUB_CONFIG`).

## Scope

- **No certificate mechanics.** `bastionhub` calls `sshca` and never holds CA
  private keys.
- **No policy schema.** Endpoints have a name, a port, a user and an optional
  identity file. There is no concept of a customer, role or environment.
- **No other transports.** SSH bastion and reverse tunnels only. Not
  Tailscale, WireGuard or DERP.

## Install

### Homebrew (macOS + Linuxbrew)

```sh
brew tap roselabs-io/tools
brew trust roselabs-io/tools   # recent Homebrew refuses third-party taps otherwise
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

The last column is the enrollment path added in v0.2. It requires no software
on the far end: the bootstrap script uses `ssh`, `ssh-keygen` and `curl` (or
PowerShell's `iwr`) only. Persistence uses systemd `Restart=always`, launchd
`KeepAlive`, or a Windows scheduled task around `ssh`, with
`ServerAliveInterval` for dead-peer detection in place of autossh.

The PowerShell path has not been tested on Windows.

## Quick start

```sh
# 0. Configure the bastion. See "Installing a bastion" below.

# 1. Create the local endpoints registry
bastionhub init

# 2. Enroll an endpoint: signs a tunnel certificate via sshca and registers it.
bastionhub endpoint enroll my-endpoint --pubkey-file ./my-endpoint.pub
# Prints the assigned port and the path of the certificate to deliver.

# 3. On the endpoint device, once the cert is in place:
#    Linux (needs sudo, systemd + apt/dnf/yum/apk):
sudo bastionhub endpoint install
sudo bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    macOS (per-user, no sudo, launchd + brew):
bastionhub endpoint install
bastionhub endpoint setup --port 12001 --bastion bastion.example.com
#    setup accepts --dry-run to print the unit or plist without installing it.

# 4. From the operator machine:
bastionhub list                       # show configured endpoints
bastionhub status                     # query bastion for live tunnels
bastionhub ssh my-endpoint            # ProxyJump through the bastion
bastionhub ssh my-endpoint -- uptime  # run a single command
```

## Installing a bastion

On a Debian or Ubuntu server:

```sh
curl -sSL https://raw.githubusercontent.com/roselabs-io/bastionhub/v0.2.0/deploy/install.sh \
    | sudo bash -s -- --domain bastion.example.io --acme-email you@example.io
```

The script creates the `gw-tunnel`, `gw-user` and `gw-passthrough` users,
writes the sshd drop-in, installs the `bastionhub` binary, runs `bastionhub
serve` under systemd, installs Caddy for TLS on `--domain`, and opens the
required ufw ports. It prints an admin token on completion.

It does not install a certificate authority. Supply the CA public key
separately:

```sh
scp ca/user_ca.pub root@bastion.example.io:/etc/ssh/user_ca.pub
ssh root@bastion.example.io 'sshd -t && systemctl reload ssh'
```

Options:

| | |
|---|---|
| `--domain <host>` | Required. Hostname devices dial and TLS is issued for. A bare IP is rejected unless `--skip-tls` is given, since no CA issues certificates for IP addresses. |
| `--user-ca <path>` | Install the CA public key during setup. `-` reads stdin. |
| `--acme-email <addr>` | Let's Encrypt contact address. |
| `--skip-tls` | Do not install Caddy. Required if another service holds `:80`/`:443`; the script refuses to displace one. |
| `--skip-sshd` | Do not modify sshd configuration. |
| `--version <tag>` | Release to install. Default: latest. |
| `--binary <path>` | Install a local binary instead of downloading a release. |

The script is idempotent; re-running preserves the admin token. The URL is
pinned to a release tag, so the file does not change between runs.

## Enrollment

`bastionhub endpoint install` and `endpoint setup` require a shell on the
target machine and `bastionhub` installed there. `bastionhub serve` and
`bastionhub invite` remove both requirements.

`serve` runs on the bastion. It issues single-use invite codes, serves a
bootstrap script, and relays a public key from the far end to the operator and
a signed certificate back. It stores no private material and cannot sign.

```sh
# On the bastion:
bastionhub serve --bastion bastion.example.io --listen 127.0.0.1:8420
# Prints an admin token on first run.

# On the machine holding the CA:
export BASTIONHUB_SERVE_URL=https://bastion.example.io
export BASTIONHUB_ADMIN_TOKEN=<token>

bastionhub invite tex-mmv2
```

`invite` prints two commands, one per platform:

```
    curl -sSL https://bastion.example.io/j/3DY2FNYT | sh      # mac / linux
    iwr -useb https://bastion.example.io/j/3DY2FNYT | iex     # windows
```

The far end runs one of them. The bootstrap script generates an ed25519
keypair locally, submits the public key, waits for a certificate, and installs
it. The private key does not leave that machine. The script requires only
`ssh`, `ssh-keygen` and `curl` (or PowerShell's `iwr`), all of which ship with
Windows 10/11, macOS and Linux.

`invite` displays the submitted key's SHA256 fingerprint and waits for
confirmation before signing. The far end prints the same fingerprint, so it can
be compared out of band. `--yes` skips the prompt.

Signing happens on the machine running `invite`, via `sshca cert sign`. That
machine must be reachable while an invite is redeemed.

### Shapes

`--shape` selects what the bootstrap script does after it installs the
certificate, and determines the principal.

| `--shape` | Principal | Default validity | Behaviour |
|---|---|---|---|
| `device` | `gw-tunnel` | `+52w` | Installs a systemd unit, launchd agent or scheduled task running `ssh -R`. Survives reboot. |
| `session` | `gw-tunnel` | `+12h` | Runs `ssh -R` in the foreground and removes the key directory on exit. |
| `access` | `gw-user` | `+12h` | Writes the certificate to `~/.ssh` and adds a `Host bastionhub` block to `~/.ssh/config`. Opens no tunnel and starts no service. |

`--principal` overrides the default and emits a warning when it does not match
the shape. The mismatch is not rejected but does not work: sshd maps a
certificate's principal to the target username, so a `gw-user` certificate
cannot authenticate as `gw-tunnel`.

An `access` certificate is not scoped to a specific endpoint. It authenticates
as `gw-user`, which may forward to any port in the tunnel range, including
endpoints enrolled after the certificate was issued. A single long-lived
`access` certificate therefore covers the whole fleet:

```sh
bastionhub invite my-work-laptop --shape access --valid +52w
```

Use `sshca cert revoke` to end it before expiry.

### serve

`serve` binds `127.0.0.1:8420` by default and expects TLS termination in front
of it. `--tls-cert` and `--tls-key` serve HTTPS directly instead.

| Flag | Default |
|---|---|
| `--bastion <host>` | required — the hostname far ends dial for SSH |
| `--listen <addr>` | `127.0.0.1:8420` |
| `--base-url <url>` | `https://<bastion>` |
| `--tls-cert`, `--tls-key` | unset |

State is written to `~/.config/bastionhub/invites.json`
(`$BASTIONHUB_SERVE_STATE`). The admin token is generated on first run and
stored at `~/.config/bastionhub/admin-token`
(`$BASTIONHUB_ADMIN_TOKEN_FILE`), mode 0600.

Invite codes are 8 characters drawn from a 31-symbol alphabet excluding `0`,
`O`, `1`, `I` and `L`. They are single-use and expire after `--ttl` (default 30
minutes). Unknown, expired, spent and revoked codes return identical responses.
Repeated failed lookups from one address are rate-limited.

## Stability promises

Pre-1.0: minor releases may break things. Breaking changes will be called out in [CHANGELOG.md](CHANGELOG.md) with the rationale.

Post-1.0: [SemVer](https://semver.org/). Three surfaces are versioned:

- **CLI grammar** — subcommand names, flag names, exit codes
- **`endpoints.yaml` schema** — fields documented in [CLAUDE.md](CLAUDE.md) "Contract surface"
- **Deploy artifact identifiers** — `bastionhub-tunnel.service` (systemd unit), `com.roselabs.bastionhub-tunnel` (launchd label), `10-bastionhub.conf` / `30-passthrough-acl.conf` (sshd drop-ins)
- **`serve` HTTP surface** — the far-end routes (`/j/<code>`, `/e/<code>/pubkey`, `/e/<code>/cert`) are the contract a bootstrap script in the wild depends on. The `/api/` routes are operator-facing and move with the CLI.

## See also

- [sshca](https://github.com/roselabs-io/sshca) — cert tool bastionhub depends on
- [deploy/bastion/](deploy/bastion/) — sshd drop-ins and the optional `AuthorizedPrincipalsCommand` script

## License

MIT. See [LICENSE](LICENSE).
