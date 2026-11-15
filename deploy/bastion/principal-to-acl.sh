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
# The principals and their permitopen destinations are read from the mapping
# file /etc/bastionhub/principal-acl ($PRINCIPAL_TO_ACL_MAP overrides it; sshd
# runs this script with an empty environment). Each non-blank line that does
# not start with '#' is:
#
#   <principal> <host:port> [<host:port> ...]
#
# and becomes one output line:
#
#   permitopen="<host:port>"[,permitopen="..."],no-pty,no-X11-forwarding,no-agent-forwarding <principal>
#
# A line whose principal or destination contains characters outside
# [A-Za-z0-9._:-], or that has no destination, is skipped and logged.
#
# Security notes:
#   - sshd requires this script to be root-owned and not group/world-writable.
#   - The script's stdout is consumed line-by-line by sshd; stderr is ignored
#     during cert auth but lands in syslog (good for debugging).
#   - The script runs as gw-passthrough (AuthorizedPrincipalsCommandUser), so
#     the mapping file must be readable by that user.

set -u

target_user="${1:-}"
key_id="${2:-}"
map_file="${PRINCIPAL_TO_ACL_MAP:-/etc/bastionhub/principal-acl}"

log() {
    logger -t principal-to-acl "$*" >&2
}

log "principal-to-acl invoked: user=$target_user key_id=$key_id"

if [ ! -r "$map_file" ]; then
    log "mapping file $map_file is missing or unreadable; no principals emitted"
    exit 1
fi

# The character check keeps a value from closing the quoted permitopen option
# or adding options of its own.
valid='^[A-Za-z0-9._:-]+$'

lineno=0
while read -r -a fields || [ "${#fields[@]}" -gt 0 ]; do
    lineno=$((lineno + 1))
    if [ "${#fields[@]}" -eq 0 ] || [[ "${fields[0]}" == '#'* ]]; then
        continue
    fi
    principal="${fields[0]}"
    if [ "${#fields[@]}" -lt 2 ]; then
        log "$map_file:$lineno: skipped: no destination for $principal"
        continue
    fi
    options=""
    for value in "${fields[@]}"; do
        if ! [[ "$value" =~ $valid ]]; then
            log "$map_file:$lineno: skipped: invalid value"
            continue 2
        fi
    done
    for dest in "${fields[@]:1}"; do
        options="${options}permitopen=\"$dest\","
    done
    echo "${options}no-pty,no-X11-forwarding,no-agent-forwarding $principal"
done <"$map_file"
