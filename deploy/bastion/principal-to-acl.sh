#!/bin/bash
#
# /usr/local/bin/principal-to-acl
#
# Invoked by sshd via AuthorizedPrincipalsCommand for the gw-passthrough user.
# Emits an authorized_principals(5)-formatted list — one line per accepted
# principal, optional authorized_keys-style options prefixed.
#
# Arguments (sshd token expansion):
#   $1 = %u — target Unix username
#   $2 = %i — certificate key_id (audit string)
#
# Current status: SKELETON — emits a small fixed set of principals for testing
# the Pattern B mechanism end-to-end. Each entry maps a principal to a
# specific permitopen restriction. A real V1+ implementation would read
# a registry (per-edge metadata) and emit one line per principal the
# bound identity is allowed to assert.
#
# Security notes:
#   - sshd requires this script to be root-owned and not group/world-writable.
#   - The script's stdout is consumed line-by-line by sshd; stderr is ignored
#     during cert auth but lands in syslog (good for debugging).

set -u

target_user="${1:-}"
key_id="${2:-}"

# Log invocations to syslog for debugging (sshd captures stderr in auth log).
logger -t principal-to-acl "principal-to-acl invoked: user=$target_user key_id=$key_id" >&2

# Emit the universe of principals this user is allowed to authenticate as.
# Each line: [options] <principal-name>
#
# sshd matches the cert's valid_principals list against the principal-name
# column. If any of the cert's principals appears here, auth succeeds and
# the corresponding options apply.

cat <<'EOF'
# Demo principals (skeleton — replace with registry-driven generation)
# gw-edge-ssh-test-loopback: forward to bastion's own sshd (127.0.0.1:22)
permitopen="127.0.0.1:22",no-pty,no-X11-forwarding,no-agent-forwarding gw-edge-ssh-test-loopback

# gw-edge-ssh-test-perso-mbp: forward to perso-mbp's reverse-tunnel port (127.0.0.1:12001)
permitopen="127.0.0.1:12001",no-pty,no-X11-forwarding,no-agent-forwarding gw-edge-ssh-test-perso-mbp
EOF
