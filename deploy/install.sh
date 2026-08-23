#!/bin/bash
# bastionhub — fresh VPS to working bastion.
#
#   curl -sSL https://get.roselabs.io/bastion | sudo bash -s -- --domain bastion.example.io
#
# What you get:
#   - the three restricted role users (gw-tunnel, gw-user, gw-passthrough)
#   - sshd configured for certificate auth, with each role scoped to what it needs
#   - the bastionhub binary
#   - `bastionhub serve` under systemd, behind Caddy with automatic HTTPS
#   - an admin token to put on the operator's laptop
#
# What it never touches: the CA. The certificate authority stays on the
# operator's machine and nothing here can sign. This host relays public keys
# and certificates between parties who cannot otherwise reach each other. If
# this box is fully compromised, the attacker gets public keys and expired
# invite codes, and cannot mint a single certificate.
#
# Re-running is safe. Every step checks before it acts.

set -euo pipefail

VERSION="${BASTIONHUB_VERSION:-latest}"
PORT_LO=12001
PORT_HI=12099
SERVE_PORT=8420
DOMAIN=""
ACME_EMAIL=""
USER_CA=""
SKIP_TLS=0
SKIP_SSHD=0
LOCAL_BIN=""

die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
note() { printf '\033[36m==>\033[0m %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
skip() { printf '  \033[90m·\033[0m %s\n' "$*"; }

usage() {
    cat <<USAGE
usage: install.sh --domain <host> [options]

  --domain <host>      Public hostname for this bastion. Required: it is both
                       what devices dial for SSH and what TLS is issued for.
  --user-ca <path|->   The operator's user CA public key. Without it, sshd
                       trusts nothing and no one can log in with a cert. May
                       be a path, or "-" to read from stdin. If omitted, the
                       script sets everything else up and tells you how to
                       finish.
  --acme-email <addr>  Contact address for Let's Encrypt.
  --skip-tls           Do not install or configure Caddy. Use when something
                       else already terminates TLS on this host.
  --skip-sshd          Do not touch sshd configuration.
  --version <tag>      bastionhub release to install (default: latest).
  --binary <path>      Install this binary instead of downloading a release.
                       For air-gapped hosts, and for testing an unreleased
                       build.
USAGE
    exit 0
}

while [ $# -gt 0 ]; do
    case "$1" in
        --domain)     DOMAIN="${2:-}"; shift 2 ;;
        --user-ca)    USER_CA="${2:-}"; shift 2 ;;
        --acme-email) ACME_EMAIL="${2:-}"; shift 2 ;;
        --version)    VERSION="${2:-}"; shift 2 ;;
        --binary)     LOCAL_BIN="${2:-}"; shift 2 ;;
        --skip-tls)   SKIP_TLS=1; shift ;;
        --skip-sshd)  SKIP_SSHD=1; shift ;;
        -h|--help)    usage ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

# --- preflight ---------------------------------------------------------------

[ -n "$DOMAIN" ] || die "--domain is required (try --help)"

# A bare IP works for SSH but not for the invite link: no CA issues
# certificates for IP addresses, so https://<ip>/j/<code> has no valid cert.
# Telling a technician to pass -k defeats the point — that URL is piped into a
# shell, and TLS is the only thing stopping someone on the path choosing what
# runs on their machine.
if printf '%s' "$DOMAIN" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
    if [ "$SKIP_TLS" = "0" ]; then
        cat >&2 <<MSG

$DOMAIN is an IP address. Let's Encrypt cannot issue a certificate for it, so
the invite link would have no valid TLS and the bootstrap command would fail
on the far end.

Either point a hostname at this box — any free domain works — or pass
--skip-tls and terminate TLS yourself.

MSG
        die "refusing to set up an invite service with no usable TLS"
    fi
    printf '\033[33mwarning:\033[0m %s is an IP. Invite links will need TLS from whatever you put in front.\n' "$DOMAIN"
fi

[ "$(id -u)" = "0" ] || die "run as root (sudo)"
command -v systemctl >/dev/null || die "this script needs systemd"
command -v apt-get   >/dev/null || die "this script supports Debian/Ubuntu; adapt it for your distro"

case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
esac

# Resolving the domain to this host is not required — split-horizon DNS and
# proxies are legitimate — but getting it wrong is the most common way for
# this to half-work, so say something.
RESOLVED=$(getent hosts "$DOMAIN" 2>/dev/null | awk '{print $1}' | head -1 || true)
if [ -z "$RESOLVED" ] && [ "$SKIP_TLS" = "0" ]; then
    printf '\033[33mwarning:\033[0m %s does not resolve yet. TLS issuance will fail until it does.\n' "$DOMAIN"
elif [ -n "$RESOLVED" ] && [ "$SKIP_TLS" = "0" ]; then
    note "$DOMAIN resolves to $RESOLVED"
fi

note "Installing bastionhub on $DOMAIN ($ARCH)"

# --- role users --------------------------------------------------------------
#
# Three accounts, each able to do exactly one thing. The split is the security
# model: a device holds gw-tunnel and may listen but gets no shell; an operator
# holds gw-user and may forward but may not listen. Neither can do the other's
# job. They authenticate by certificate only — authorized_keys stays empty.

note "Role users"
for u in gw-tunnel gw-user gw-passthrough; do
    if id "$u" >/dev/null 2>&1; then
        skip "$u exists"
    else
        useradd -m -s /usr/sbin/nologin "$u"
        ok "created $u"
    fi
    install -d -m 700 -o "$u" -g "$u" "/home/$u/.ssh"
    : > "/home/$u/.ssh/authorized_keys"
    chown "$u:$u" "/home/$u/.ssh/authorized_keys"
    chmod 600 "/home/$u/.ssh/authorized_keys"
done

# --- is something already holding the web ports? -----------------------------
#
# A bastion is often not a dedicated box. If something already owns :80/:443 —
# a Docker reverse proxy, an existing nginx — installing another web server
# breaks whatever is there. Refuse rather than fight it, and say what to do.

if [ "$SKIP_TLS" = "0" ]; then
    HOLDER=""
    if command -v ss >/dev/null; then
        HOLDER=$(ss -ltnp 2>/dev/null | awk '$4 ~ /:(80|443)$/ {print $NF}' | head -1 || true)
    fi
    if [ -n "$HOLDER" ] && ! echo "$HOLDER" | grep -q caddy; then
        cat >&2 <<MSG

Something already listens on :80/:443 on this host:
    $HOLDER

Installing another web server would break it. Two ways forward:

  1. Point the existing proxy at bastionhub and re-run with --skip-tls:

         $DOMAIN {
             reverse_proxy 127.0.0.1:$SERVE_PORT
         }

     (nginx: proxy_pass http://127.0.0.1:$SERVE_PORT;)

     If that proxy runs in a container, 127.0.0.1 is the container — use the
     host's bridge address, and allow it through the firewall.

  2. Run bastionhub on a host of its own.

MSG
        die "refusing to take ports another service is using"
    fi
    [ -n "$HOLDER" ] && note "existing Caddy detected — will add a site to it rather than replace it"
fi

# --- sshd --------------------------------------------------------------------

if [ "$SKIP_SSHD" = "1" ]; then
    note "sshd: skipped (--skip-sshd)"
else
    note "sshd configuration"

    if [ -n "$USER_CA" ]; then
        if [ "$USER_CA" = "-" ]; then
            cat > /etc/ssh/user_ca.pub
        else
            [ -f "$USER_CA" ] || die "no such file: $USER_CA"
            cp "$USER_CA" /etc/ssh/user_ca.pub
        fi
        chmod 644 /etc/ssh/user_ca.pub
        # A private key here would be a catastrophe, and it is an easy slip:
        # the file is one tab-completion away from the public one.
        if grep -q "PRIVATE KEY" /etc/ssh/user_ca.pub; then
            rm -f /etc/ssh/user_ca.pub
            die "that is a PRIVATE key. The CA private key must never leave the operator's machine. Pass the .pub."
        fi
        ok "trusting CA $(ssh-keygen -lf /etc/ssh/user_ca.pub | awk '{print $2}')"
    elif [ -f /etc/ssh/user_ca.pub ]; then
        skip "using existing /etc/ssh/user_ca.pub"
    else
        printf '\033[33mwarning:\033[0m no user CA installed — nobody can authenticate yet.\n'
        printf '          finish with: scp ca/user_ca.pub root@%s:/etc/ssh/user_ca.pub\n' "$DOMAIN"
        touch /etc/ssh/user_ca.pub
    fi

    [ -f /etc/ssh/revoked_keys.krl ] || : > /etc/ssh/revoked_keys.krl

    # PermitListen and PermitOpen both reject port ranges — "12001-12099" is a
    # syntax error, not a range — and PermitOpen matches the requested host as
    # a literal string. Hence the enumeration, generated here rather than
    # written out, because 200 hand-maintained entries rot.
    LISTEN_PORTS=""; OPEN_PORTS=""
    for p in $(seq $PORT_LO $PORT_HI); do
        LISTEN_PORTS="$LISTEN_PORTS $p"
        OPEN_PORTS="$OPEN_PORTS 127.0.0.1:$p localhost:$p"
    done

    cat > /etc/ssh/sshd_config.d/10-bastionhub.conf <<CONF
# /etc/ssh/sshd_config.d/10-bastionhub.conf
# Written by bastionhub install.sh. Regenerated on re-run — edit the installer,
# not this file.
#
# The security model is these two Match blocks. A device holds a cert with
# principal gw-tunnel and may listen on a tunnel port; it gets no shell and
# cannot forward. An operator holds gw-user and may forward to a tunnel port;
# they get no shell and cannot listen. Neither can do the other's job.
#
# OpenSSH maps a certificate's principal to the target username, which is why
# these are "Match User" and not "Match Principal" — the latter is not valid
# syntax, a fact that costs everyone an afternoon exactly once.

PasswordAuthentication no
KbdInteractiveAuthentication no

# Drop dead connections. Without this a flapping endpoint holds its port until
# sshd notices, and the reconnect fails to bind.
ClientAliveInterval 30
ClientAliveCountMax 3

TrustedUserCAKeys /etc/ssh/user_ca.pub
RevokedKeys /etc/ssh/revoked_keys.krl

Match User gw-tunnel
    AuthenticationMethods publickey
    PasswordAuthentication no
    AllowTcpForwarding remote
    PermitListen$LISTEN_PORTS
    PermitOpen none
    ForceCommand /bin/false
    PermitTTY no
    X11Forwarding no
    AllowAgentForwarding no

Match User gw-user
    AuthenticationMethods publickey
    PasswordAuthentication no
    AllowTcpForwarding local
    PermitListen none
    PermitOpen$OPEN_PORTS
    ForceCommand /bin/false
    PermitTTY no
    X11Forwarding no
    AllowAgentForwarding no
CONF
    chmod 644 /etc/ssh/sshd_config.d/10-bastionhub.conf

    if [ -f /etc/ssh/ssh_host_ed25519_key-cert.pub ]; then
        echo "HostCertificate /etc/ssh/ssh_host_ed25519_key-cert.pub" \
            >> /etc/ssh/sshd_config.d/10-bastionhub.conf
        ok "host certificate enabled"
    fi

    # sshd -t needs this and will not create it. Absent on a box where sshd has
    # never run, which is exactly where this script gets used.
    [ -d /run/sshd ] || install -d -m 755 /run/sshd

    if ! SSHD_ERR=$(sshd -t 2>&1); then
        printf '\033[31merror:\033[0m sshd rejected the configuration. Nothing was reloaded.\n' >&2
        printf '%s\n' "$SSHD_ERR" | sed 's/^/    /' >&2
        # Leave the file behind: reading it is how anyone diagnoses this.
        die "see /etc/ssh/sshd_config.d/10-bastionhub.conf"
    fi
    ok "config valid"

    # Editing sshd remotely is the classic way to lock yourself out of a box
    # with no console. Restore automatically unless this script gets to the end.
    RESTORE=/run/bastionhub-sshd-restore
    mkdir -p "$RESTORE"
    cp /etc/ssh/sshd_config.d/10-bastionhub.conf "$RESTORE/" 2>/dev/null || true
    cat > "$RESTORE/undo.sh" <<'UNDO'
#!/bin/bash
rm -f /etc/ssh/sshd_config.d/10-bastionhub.conf
sshd -t && systemctl reload ssh
logger "bastionhub install: sshd config auto-reverted (installer did not finish)"
UNDO
    chmod +x "$RESTORE/undo.sh"
    systemd-run --on-active=5min --unit=bastionhub-sshd-undo "$RESTORE/undo.sh" >/dev/null 2>&1 || true

    # Debian calls it ssh.service, RHEL-alikes sshd.service, and on a truly
    # fresh image it may not be running at all.
    SSHD_UNIT=""
    for u in ssh sshd; do
        systemctl list-unit-files "$u.service" >/dev/null 2>&1 && \
            systemctl cat "$u.service" >/dev/null 2>&1 && { SSHD_UNIT=$u; break; }
    done
    [ -n "$SSHD_UNIT" ] || die "no ssh/sshd systemd unit found"

    if systemctl is-active --quiet "$SSHD_UNIT"; then
        systemctl reload "$SSHD_UNIT"
        ok "sshd reloaded (auto-revert armed for 5 minutes)"
    else
        systemctl enable --now "$SSHD_UNIT" >/dev/null 2>&1 || true
        ok "sshd started"
    fi
fi

# --- the binary --------------------------------------------------------------

note "bastionhub binary"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if [ -n "$LOCAL_BIN" ]; then
    [ -f "$LOCAL_BIN" ] || die "no such file: $LOCAL_BIN"
    BIN="$LOCAL_BIN"
    ok "using $LOCAL_BIN"
else
if [ "$VERSION" = "latest" ]; then
    VERSION=$(curl -fsSL https://api.github.com/repos/roselabs-io/bastionhub/releases/latest \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
    [ -n "$VERSION" ] || die "could not determine the latest release; pass --version"
fi
TARBALL="bastionhub-${VERSION}-linux-${ARCH}.tar.gz"
BASE="https://github.com/roselabs-io/bastionhub/releases/download/${VERSION}"

if ! curl -fsSL "$BASE/$TARBALL" -o "$TMP/$TARBALL"; then
    die "download failed: $BASE/$TARBALL"
fi

# Every release ships checksums.txt. Skipping the check would mean a script
# people run as root taking a binary on faith.
if curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null; then
    WANT=$(awk -v f="$TARBALL" '$2 == f || $2 == "*"f {print $1}' "$TMP/checksums.txt" | head -1)
    if [ -n "$WANT" ]; then
        GOT=$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')
        [ "$WANT" = "$GOT" ] || die "checksum mismatch for $TARBALL (expected $WANT, got $GOT)"
        ok "checksum verified"
    else
        printf '\033[33mwarning:\033[0m %s not listed in checksums.txt\n' "$TARBALL"
    fi
else
    printf '\033[33mwarning:\033[0m no checksums.txt in this release; binary unverified\n'
fi

tar -xzf "$TMP/$TARBALL" -C "$TMP"
BIN=$(find "$TMP" -type f -name bastionhub | head -1)
[ -n "$BIN" ] || die "no bastionhub binary in $TARBALL"
fi

# `serve` is what this script exists to install. Releases before it exist will
# install fine and then fail at the systemd step with an unhelpful usage dump.
if ! "$BIN" serve --help >/dev/null 2>&1; then
    die "this bastionhub build has no 'serve' command ($("$BIN" --version 2>&1)). Use --version with a release that includes it, or --binary with a build of your own."
fi

# Overwriting a running binary in place fails with ETXTBSY. Stage and swap.
install -m 755 "$BIN" /usr/local/bin/bastionhub.new
if systemctl is-active --quiet bastionhub-serve 2>/dev/null; then
    systemctl stop bastionhub-serve
fi
mv /usr/local/bin/bastionhub.new /usr/local/bin/bastionhub
ok "installed $(/usr/local/bin/bastionhub --version)"

# --- serve -------------------------------------------------------------------
#
# Runs on the host rather than in a container: it is deliberately close to
# nothing, and a container would be one more thing to keep current on the most
# exposed machine in the system.

note "invite service"
BIND_ADDR="127.0.0.1"
if [ "$SKIP_TLS" = "0" ] && command -v docker >/dev/null 2>&1 && docker ps >/dev/null 2>&1; then
    # A containerised proxy cannot reach 127.0.0.1 on the host.
    BIND_ADDR=$(ip -4 addr show docker0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1 | head -1)
    BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
fi

cat > /etc/systemd/system/bastionhub-serve.service <<UNIT
[Unit]
Description=bastionhub invite/relay service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/bastionhub serve --bastion $DOMAIN --listen $BIND_ADDR:$SERVE_PORT --base-url https://$DOMAIN
Restart=always
RestartSec=5
User=root

StateDirectory=bastionhub
StateDirectoryMode=0700
Environment=BASTIONHUB_SERVE_STATE=/var/lib/bastionhub/invites.json
Environment=BASTIONHUB_ADMIN_TOKEN_FILE=/var/lib/bastionhub/admin-token

# This process holds no CA and signs nothing. Give it correspondingly little.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
MemoryMax=256M

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now bastionhub-serve >/dev/null 2>&1
sleep 2
systemctl is-active --quiet bastionhub-serve || {
    journalctl -u bastionhub-serve -n 20 --no-pager >&2
    die "bastionhub-serve failed to start"
}
ok "serve listening on $BIND_ADDR:$SERVE_PORT"

# --- TLS ---------------------------------------------------------------------
#
# The invite line tells a stranger to pipe a URL into their shell. Without TLS,
# anyone on the path chooses what they run. This is not optional in practice.

if [ "$SKIP_TLS" = "1" ]; then
    note "TLS: skipped (--skip-tls). Point your proxy at $BIND_ADDR:$SERVE_PORT."
else
    note "TLS"
    if ! command -v caddy >/dev/null 2>&1; then
        apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null
        curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
            | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null
        echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
            > /etc/apt/sources.list.d/caddy-stable.list
        apt-get update -qq
        apt-get install -y -qq caddy >/dev/null
        ok "installed caddy"
    else
        skip "caddy present"
    fi

    SITE=/etc/caddy/Caddyfile.d/bastionhub.caddy
    mkdir -p /etc/caddy/Caddyfile.d
    {
        [ -n "$ACME_EMAIL" ] && printf '{\n\temail %s\n}\n\n' "$ACME_EMAIL"
        printf '%s {\n\treverse_proxy %s:%s\n}\n' "$DOMAIN" "$BIND_ADDR" "$SERVE_PORT"
    } > "$SITE"

    # Debian's Caddyfile does not import a site directory by default.
    if ! grep -q "Caddyfile.d" /etc/caddy/Caddyfile 2>/dev/null; then
        printf '\nimport /etc/caddy/Caddyfile.d/*.caddy\n' >> /etc/caddy/Caddyfile
    fi

    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 \
        || die "Caddy config invalid — nothing reloaded"
    systemctl enable caddy >/dev/null 2>&1
    systemctl reload caddy 2>/dev/null || systemctl restart caddy
    ok "caddy serving $DOMAIN"
fi

# --- firewall ----------------------------------------------------------------

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "^Status: active"; then
    note "firewall"
    for rule in "22/tcp" "80/tcp" "443/tcp"; do
        ufw status | grep -q "^$rule" || { ufw allow "$rule" >/dev/null; ok "allowed $rule"; }
    done
    # Containerised proxies reach the host over the bridge, and ufw's policy is
    # DROP. Without this the proxy times out and every invite 502s.
    if [ "$BIND_ADDR" != "127.0.0.1" ]; then
        ufw status | grep -q "$SERVE_PORT" || {
            ufw allow from 172.16.0.0/12 to any port "$SERVE_PORT" proto tcp >/dev/null
            ok "allowed $SERVE_PORT from container networks"
        }
    fi
fi

# --- done --------------------------------------------------------------------

systemctl stop bastionhub-sshd-undo.timer 2>/dev/null || true
systemctl reset-failed bastionhub-sshd-undo.timer 2>/dev/null || true
rm -rf /run/bastionhub-sshd-restore

TOKEN=$(cat /var/lib/bastionhub/admin-token 2>/dev/null || echo "<unavailable>")

cat <<DONE

$(printf '\033[32m✓\033[0m') bastion ready at https://$DOMAIN

On the operator's laptop — the machine that holds the CA:

    export BASTIONHUB_SERVE_URL=https://$DOMAIN
    export BASTIONHUB_ADMIN_TOKEN=$TOKEN

Then invite a machine. Two directions:

    bastionhub invite tex-controller --shape device
        a machine that must be REACHABLE, and stays

    bastionhub invite my-work-laptop --shape access --valid +52w
        a machine that needs to REACH the fleet. Issue this once; every
        device you enrol later is reachable from it with no re-issue.

DONE

if [ ! -s /etc/ssh/user_ca.pub ]; then
    cat <<PENDING
$(printf '\033[33m!\033[0m') No CA is trusted yet, so nobody can authenticate. From the operator's machine:

    scp ca/user_ca.pub root@$DOMAIN:/etc/ssh/user_ca.pub
    ssh root@$DOMAIN 'sshd -t && systemctl reload ssh'

PENDING
fi

cat <<'FOOTER'
The CA is not on this host and never should be. It stays on the operator's
machine; this box relays public keys and certificates. A full compromise here
yields public material and expired invite codes, and cannot mint a certificate.

FOOTER
