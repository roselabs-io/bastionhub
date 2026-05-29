# Bastion deploy artifacts

Files in this directory are shipped to the bastion VPS during setup or as
part of a feature rollout. They live in-repo so the deploy steps are
reproducible from scratch.

## What's here

| File | Deploy target | Purpose |
|---|---|---|
| [`principal-to-acl.sh`](principal-to-acl.sh) | `/usr/local/bin/principal-to-acl` (root:root, 755) | `AuthorizedPrincipalsCommand` script for `gw-passthrough` user — emits per-principal ACL lines (Pattern B) |
| [`30-passthrough-acl.conf`](30-passthrough-acl.conf) | `/etc/ssh/sshd_config.d/30-passthrough-acl.conf` (root:root, 644) | sshd drop-in wiring the script into a `Match User gw-passthrough` block |

## What's NOT here yet (active backlog item)

A complete starter sshd config — the foundational `Match User gw-tunnel`, `Match User gw-user`, `TrustedUserCAKeys`, `RevokedKeys` blocks. These currently live as manual setup on Patrick's bastion. Bastionhub will ship `00-bastionhub.conf` as the starter once the work lands. See [`../../docs/planning/backlog.md`](../../docs/planning/backlog.md) item #1.

## Deploy

### Pattern B (`AuthorizedPrincipalsCommand`) — per-principal scoping

Prerequisites: bastion already has cert-auth configured (`TrustedUserCAKeys` pointing at sshca's `user_ca.pub`, `Match User gw-tunnel` and `Match User gw-user` blocks).

```bash
# 1. Create the gw-passthrough Unix user (no shell)
ssh bastion-root 'useradd -m -s /usr/sbin/nologin gw-passthrough || echo already exists'

# 2. Ship the script with the right perms (sshd refuses to execute otherwise)
scp deploy/bastion/principal-to-acl.sh bastion-root:/usr/local/bin/principal-to-acl
ssh bastion-root 'chown root:root /usr/local/bin/principal-to-acl && chmod 755 /usr/local/bin/principal-to-acl'

# 3. Ship the sshd drop-in + validate + reload
scp deploy/bastion/30-passthrough-acl.conf bastion-root:/etc/ssh/sshd_config.d/30-passthrough-acl.conf
ssh bastion-root 'sshd -t && systemctl reload ssh && echo reloaded'
```

### Verify

```bash
# Sign a test cert with one of the demo principals
sshca cert sign --ca user --principal gw-edge-ssh-test-loopback \
    --valid +1h --key-id 'test-passthrough-pattern' \
    --dir ./ca /path/to/test-key.pub

# Try to forward through bastion as gw-passthrough to bastion's own loopback
ssh -J bastion-root@<bastion-host> gw-passthrough@<bastion-host> \
    -W 127.0.0.1:22 -i /path/to/test-key
# (the test-key-cert.pub next to it is auto-loaded)
```

The connection should succeed (forward to 127.0.0.1:22, which is the bastion's own sshd loopback). A forward to a different port should be refused.

## See also

- Upstream [ADR-004](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-004-principal-taxonomy-default-no-shell.md) — Principal taxonomy
- Upstream [ADR-008](https://github.com/roselabs-io/gateway/blob/main/docs/decisions/ADR-008-extract-bastion-substrate-as-bastionhub.md) — Why per-principal scoping needs `AuthorizedPrincipalsCommand`
- [github.com/roselabs-io/sshca](https://github.com/roselabs-io/sshca) — Cert tool used by `bastionhub endpoint enroll`
