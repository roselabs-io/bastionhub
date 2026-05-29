# Bastion deploy artifacts

Files in this directory are shipped to the bastion VPS during setup. They live in-repo so the deploy steps are reproducible from `git clone`.

## What's here

| File | Deploy target | Purpose |
|---|---|---|
| [`10-bastionhub.conf`](10-bastionhub.conf) | `/etc/ssh/sshd_config.d/10-bastionhub.conf` (root:root, 644) | **Foundational**: CA trust + `Match User gw-tunnel` + `Match User gw-user` blocks |
| [`principal-to-acl.sh`](principal-to-acl.sh) | `/usr/local/bin/principal-to-acl` (root:root, 755) | **Optional Pattern B**: `AuthorizedPrincipalsCommand` script — emits per-principal ACL lines |
| [`30-passthrough-acl.conf`](30-passthrough-acl.conf) | `/etc/ssh/sshd_config.d/30-passthrough-acl.conf` (root:root, 644) | **Optional Pattern B**: sshd drop-in wiring the script into a `Match User gw-passthrough` block |

The 10-bastionhub.conf shipped here is the minimum-viable bastion for the bastionhub V0 substrate. Pattern B (30-passthrough-acl.conf + principal-to-acl.sh) is layered on top when you need per-principal `permitopen` scoping for the `gw-passthrough` role.

## Prerequisites

- A Linux VPS running OpenSSH (any reasonably recent distro)
- Root SSH access to the bastion as `bastion-root` (your own raw-key initial bootstrap)
- [`sshca`](https://github.com/roselabs-io/sshca) installed on the engineer laptop with a CA initialized (`sshca ca init`)

## Deploy

### Step 1 — Foundational config (10-bastionhub.conf)

This brings the bastion up as a cert-auth-only SSH bastion with two role blocks (`gw-tunnel` for endpoint dial-in, `gw-user` for ProxyJump).

```bash
# 0. (One-time) Generate CAs on engineer laptop if not already done
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

### Step 2 (optional) — Pattern B for per-principal scoping (gw-passthrough)

Only needed if you'll scope tunnel-through forwarding *per principal* (e.g. one principal per downstream edge device). For the gw-tunnel + gw-user roles, Step 1 alone is enough.

```bash
# 1. Create the gw-passthrough Unix user (no shell)
ssh bastion-root 'useradd -m -s /usr/sbin/nologin gw-passthrough || true'

# 2. Ship the script with the right perms (sshd refuses to execute otherwise)
scp deploy/bastion/principal-to-acl.sh bastion-root:/usr/local/bin/principal-to-acl
ssh bastion-root 'chown root:root /usr/local/bin/principal-to-acl && chmod 755 /usr/local/bin/principal-to-acl'

# 3. Ship the sshd drop-in + reload
scp deploy/bastion/30-passthrough-acl.conf bastion-root:/etc/ssh/sshd_config.d/30-passthrough-acl.conf
ssh bastion-root 'sshd -t && systemctl reload ssh && echo reloaded'
```

### Step 3 — Sign your first user cert + verify

```bash
# Sign a test gw-user cert for the engineer laptop
sshca cert sign --ca user --principal gw-user \
    --valid +8h --key-id "engineer-bastion-$(date -u +%Y%m%dT%H%MZ)" \
    --dir ./ca ~/.ssh/id_ed25519.pub

# Try a ProxyJump (the *-cert.pub is auto-loaded next to the private key)
ssh -J gw-user@<bastion-host> nobody@nothing 2>&1 | head -1
# Should NOT say "Permission denied (publickey)" — cert auth should succeed
# at the bastion. The downstream connection failing is expected (no endpoint).
```

### Verify Pattern B (optional)

```bash
# Sign a test cert with one of the demo principals in principal-to-acl.sh
sshca cert sign --ca user --principal gw-edge-ssh-test-loopback \
    --valid +1h --key-id 'test-passthrough-pattern' \
    --dir ./ca /path/to/test-key.pub

# Forward through bastion as gw-passthrough to bastion's own loopback
ssh -J bastion-root@<bastion-host> gw-passthrough@<bastion-host> \
    -W 127.0.0.1:22 -i /path/to/test-key
# Should succeed. A forward to a different port should be refused.
```

## See also

- Upstream [ADR-001](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-001-replace-raw-key-auth-with-ssh-certs.md) — `Match Principal` is not valid OpenSSH syntax; we use `Match User <role>` instead
- Upstream [ADR-004](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-004-principal-taxonomy-default-no-shell.md) — Principal taxonomy + why default principals grant no shell
- Upstream [ADR-008](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-008-extract-bastion-substrate-as-bastionhub.md) — Why per-principal scoping needs `AuthorizedPrincipalsCommand` (Pattern B)
- [github.com/roselabs-io/sshca](https://github.com/roselabs-io/sshca) — Cert tool used throughout these procedures
