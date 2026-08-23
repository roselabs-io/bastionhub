# bastionhub — contributor notes

SSH bastion and reverse-tunnel manager. Single Go binary, two dependencies
(`urfave/cli/v3` v3.9.0 and `gopkg.in/yaml.v3` v3.0.1). Certificate operations
are delegated to [sshca](https://github.com/roselabs-io/sshca), which is a
runtime dependency.

Operator-side commands run on Linux, macOS and Windows. `endpoint install` and
`endpoint setup` run on Linux and macOS.

## Scope

Out of scope, deliberately:

- **Certificate mechanics.** `endpoint enroll` and `invite` shell out to
  `sshca`. No CA private key is ever held here.
- **Policy.** `endpoints.yaml` carries port, user, identity and description.
  There is no concept of a customer, role or environment.
- **Other transports.** SSH bastion and reverse tunnels only, not Tailscale,
  WireGuard or DERP.
- **Observability.** Beyond `status` and sshd's own connection logs.

## Contract surface

Three things callers depend on. Breaking any requires a major version bump.

**CLI grammar** — subcommand names, flag names, argument positions, exit codes.
`bastionhub --help` is canonical.

**`endpoints.yaml` schema** at `~/.config/bastionhub/endpoints.yaml`
(`$BASTIONHUB_CONFIG`), mode 0600:

```yaml
bastion_alias: <string>      # default "bastion"; ProxyJump target
admin_alias:   <string>      # default "bastion-root"; used by status

endpoints:
  <name>:
    port:        <int>       # required; 12001-12099 by convention
    user:        <string>    # required
    identity:    <string>    # optional; SSH key for the hop to the endpoint
    description: <string>    # optional
```

Unknown keys are tolerated; fields may be added in a minor release.

**Deploy artifact identifiers**, which external monitoring may key on:

- systemd unit `bastionhub-tunnel.service`
- launchd label `com.roselabs.bastionhub-tunnel`
- sshd drop-ins `10-bastionhub.conf`, `30-passthrough-acl.conf`

`serve`'s far-end routes — `/j/<code>`, `/e/<code>/pubkey`, `/e/<code>/cert` —
are also a contract, because a bootstrap script already running in the wild
depends on them. The `/api/` routes move with the CLI.

## Constraints worth knowing

- **`Match Principal` is not valid `sshd_config` syntax.** `Match` accepts
  `User`, `Group`, `Host`, `LocalAddress`, `LocalPort`, `RDomain` and `Address`
  only. Role enforcement uses `Match User`, because OpenSSH requires a
  certificate's principal to match the target username.
- **Neither `PermitListen` nor `PermitOpen` accepts a port range.**
  `12001-12099` is a syntax error. `PermitOpen` also matches the requested host
  as a literal string, so `127.0.0.1:<port>` and `localhost:<port>` both need
  listing. `deploy/install.sh` generates both lists.
- **Don't reintroduce `syscall.Exec` in `sshCmd`.** It is Unix-only. The
  `exec.Command` plus `Run()` pattern with inherited stdio is correct on
  Windows and forwards signals properly.
- **`loadConfig` must re-initialise the `Endpoints` map after unmarshalling.**
  YAML with `endpoints:` and only commented children unmarshals to a nil map,
  and the first register then panics. Covered by
  `TestLoadConfig_NilMapRegression`.
- **The session bootstrap script must not `exec ssh`.** `exec` replaces the
  shell and discards the `EXIT` trap, leaving the private key on a machine that
  was promised nothing would remain. Covered by
  `TestSessionScriptDoesNotExecAwayItsCleanup`.
- **`version` in `main.go` is a `var`, not a `const`**, so release builds can
  inject the tag with `-ldflags "-X main.version=<tag>"`.

## Conventions

- Filenames: kebab-case.
- Dates: ISO `YYYY-MM-DD`.
- No YAML frontmatter in Markdown.
