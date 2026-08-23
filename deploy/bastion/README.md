# Bastion deploy artifacts

Files installed on the bastion. [`../install.sh`](../install.sh) deploys the
first of them automatically; this page documents the manual procedure and the
optional per-principal scoping, which the installer does not configure.

## What's here

| File | Deploy target | Purpose |
|---|---|---|
| [`10-bastionhub.conf`](10-bastionhub.conf) | `/etc/ssh/sshd_config.d/10-bastionhub.conf` (root:root, 644) | CA trust, host-wide settings, and the `gw-tunnel` and `gw-user` Match blocks |
| [`principal-to-acl.sh`](principal-to-acl.sh) | `/usr/local/bin/principal-to-acl` (root:root, 755) | Optional. `AuthorizedPrincipalsCommand` script emitting per-principal `permitopen` lines |
| [`30-passthrough-acl.conf`](30-passthrough-acl.conf) | `/etc/ssh/sshd_config.d/30-passthrough-acl.conf` (root:root, 644) | Optional. Wires that script into a `Match User gw-passthrough` block |

`10-bastionhub.conf` is sufficient on its own. The other two are needed only
for per-principal `permitopen` scoping, where a distinct principal is issued per
downstream device rather than granting the whole tunnel port range.

## Prerequisites

- A Linux host running OpenSSH 8.2 or later
- Root SSH access to it, aliased below as `bastion-root`
- [`sshca`](https://github.com/roselabs-io/sshca) on the operator machine, with a CA created (`sshca ca init`)

## Deploy

### Step 1 — sshd configuration

Configures certificate authentication and the two role blocks: `gw-tunnel` for
endpoints opening reverse tunnels, `gw-user` for ProxyJump.

```bash
# 0. Create the CAs on the operator machine, if not already done
sshca ca init --dir ./ca

# 1. Ship the user CA pubkey (trust root)
scp ca/user_ca.pub bastion-root:/etc/ssh/user_ca.pub

# 2. (Optional) Ship the host CA pubkey if you'll sign host certs
scp ca/host_ca.pub bastion-root:/etc/ssh/host_ca.pub

# 3. Create the role Unix users (no shell)
ssh bastion-root 'useradd -m -s /usr/sbin/nologin gw-tunnel || true'
ssh bastion-root 'useradd -m -s /usr/sbin/nologin gw-user   || true'

# 4. Ship the sshd drop-in
scp deploy/bastion/10-bastionhub.conf bastion-root:/etc/ssh/sshd_config.d/10-bastionhub.conf

# 5. Validate + reload
ssh bastion-root 'sshd -t && systemctl reload ssh && echo reloaded'
```

### Step 2 (optional) — per-principal scoping

Needed only to scope forwarding per principal, for example one principal per
downstream device. Step 1 alone covers the `gw-tunnel` and `gw-user` roles.

```bash
# 1. Create the gw-passthrough Unix user (no shell)
ssh bastion-root 'useradd -m -s /usr/sbin/nologin gw-passthrough || true'

# 2. Install the script. sshd refuses to run it unless it is root-owned and 755.
scp deploy/bastion/principal-to-acl.sh bastion-root:/usr/local/bin/principal-to-acl
ssh bastion-root 'chown root:root /usr/local/bin/principal-to-acl && chmod 755 /usr/local/bin/principal-to-acl'

# 3. Ship the sshd drop-in + reload
scp deploy/bastion/30-passthrough-acl.conf bastion-root:/etc/ssh/sshd_config.d/30-passthrough-acl.conf
ssh bastion-root 'sshd -t && systemctl reload ssh && echo reloaded'
```

### Step 3 — Sign your first user cert + verify

```bash
# Sign a gw-user certificate for the operator machine
sshca cert sign --ca user --principal gw-user \
    --valid +8h --key-id "engineer-bastion-$(date -u +%Y%m%dT%H%MZ)" \
    --dir ./ca ~/.ssh/id_ed25519.pub

# Try a ProxyJump (the *-cert.pub is auto-loaded next to the private key)
ssh -J gw-user@<bastion-host> nobody@nothing 2>&1 | head -1
# "Permission denied (publickey)" means certificate authentication failed.
# Any other error means it succeeded; the downstream connection is expected
# to fail, as no endpoint is enrolled.
```

### Verify per-principal scoping (optional)

```bash
# Sign a certificate with one of the principals listed in principal-to-acl.sh
sshca cert sign --ca user --principal gw-edge-ssh-test-loopback \
    --valid +1h --key-id 'test-passthrough-pattern' \
    --dir ./ca /path/to/test-key.pub

# Forward through bastion as gw-passthrough to bastion's own loopback
ssh -J bastion-root@<bastion-host> gw-passthrough@<bastion-host> \
    -W 127.0.0.1:22 -i /path/to/test-key
# Should succeed. A forward to a different port should be refused.
```

## See also

- [`../install.sh`](../install.sh) — automates Step 1
- [github.com/roselabs-io/sshca](https://github.com/roselabs-io/sshca) — the certificate tool used above

`Match Principal` is not valid `sshd_config` syntax. `Match` accepts `User`,
`Group`, `Host`, `LocalAddress`, `LocalPort`, `RDomain` and `Address` only,
which is why role enforcement uses `Match User`: OpenSSH requires a
certificate's principal to match the target username.
