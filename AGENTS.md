# AGENTS.md

Instructions for agent sessions working in this repository. `pipeline.yaml` names this file for
`docs.agents`, `docs.contract`, `docs.review_posture` and `docs.operating_model`.

The scope, the contract surface (CLI grammar, `endpoints.yaml`, deploy artifact identifiers,
`serve` routes), the constraints and the conventions are the sections [Scope](#scope), [Contract
surface](#contract-surface), [Constraints worth knowing](#constraints-worth-knowing) and
[Conventions](#conventions).

## Repository

A Go CLI (`go.mod`: `go 1.25.5`; dependencies `urfave/cli/v3`, `gopkg.in/yaml.v3` and
`github.com/modelcontextprotocol/go-sdk`). Certificate
operations shell out to `sshca` at runtime. `bastionhub serve` also runs on the bastion as the
systemd unit `bastionhub-serve.service`.

| Path | Content |
|---|---|
| `main.go` | the CLI; `version` is a `var` set with `-ldflags "-X main.version=<tag>"` at release |
| `serve.go` | `bastionhub serve` (the invite and relay service) and `bastionhub invite` |
| `main_test.go`, `serve_test.go` | the tests (`go test ./...`) |
| `principal-to-acl_test.go` | the tests of `deploy/bastion/principal-to-acl.sh` (`go test ./...`) |
| `mcp/server.go` | the MCP tools and the HTTP handler of `bastionhub mcp` |
| `mcp/server_test.go` | the tests of the MCP tools and the HTTP handler, through the SDK client (`go test ./...`) |
| `docs/mcp.md` | the MCP server's tools, transports and HTTP token |
| `go.mod`, `go.sum` | the module `github.com/roselabs-io/bastionhub` (`go 1.25.5`) and the locked dependencies |
| `justfile` | the recipes (Commands) |
| `deploy/install.sh` | the bastion installer for Debian and Ubuntu |
| `deploy/bastion/` | sshd drop-ins and `principal-to-acl.sh` installed on the bastion; `README.md` describes them |
| `README.md`, `CHANGELOG.md`, `AGENTS.md` | usage, release notes, contributor notes ([Scope](#scope), [Contract surface](#contract-surface), [Constraints worth knowing](#constraints-worth-knowing) and [Conventions](#conventions)) |
| `CLAUDE.md` | the single line `@AGENTS.md`, which Claude Code imports |
| `.gemini/settings.json` | `context.fileName` set to `AGENTS.md`, Gemini CLI's context file |
| `LICENSE` | the MIT license text |
| `.github/workflows/bastionhub-audit.yml` | `just audit` on a pull request or a push to `main` that changes `go.mod` or `go.sum`, weekly on `main`, and on manual dispatch |
| `.github/workflows/release.yml` | on a `v*` tag push, or called by `bastionhub-ci.yml` with a tag: builds six targets, archives, `checksums.txt`, a GitHub Release, then a `formula-update` event to `roselabs-io/homebrew-tools` |
| `.github/workflows/bastionhub-ci.yml` | `validate` (PR metadata) and `gate` (`just gate`); on a push to `main`, `tag` and `release` (Releases) |
| `.secrets.baseline` | the detect-secrets baseline `just secrets` reads: findings that are not secrets |
| `.gitignore` | the build artifacts, IDE and OS files it ignores |
| `.github/workflows/bastionhub-pm.yml` | the issue and board sync of Governor |
| `pipeline.yaml` | the Governor dispatcher's config for this repository |
| `tools/secret_scan.py` | the secret scan `just secrets` runs |
| Governor's package preflight (`tools/local_preflight.py` of the Governor package, run by the dispatcher; this repository keeps no copy) | `just check` and selected tests of the diff; CI runs `just gate` |

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

## Releases

A merge into `main` releases when it adds a version heading to `CHANGELOG.md`; it deploys nothing.
To release, the PR renames `## [Unreleased]` to `## [X.Y.Z] — YYYY-MM-DD` (and opens a new empty
`## [Unreleased]` above it).

On a push to `main`, after `gate` passes, the `tag` job of `bastionhub-ci.yml` reads the newest
heading of the form `## [X.Y.Z]`. When the tag `vX.Y.Z` does not exist on `origin`, it pushes that
tag on the pushed commit with `GITHUB_TOKEN`, and the `release` job calls `release.yml` with the tag.
When the tag exists, nothing is released. A tag pushed with `GITHUB_TOKEN` starts no workflow, which
is why `release.yml` is called rather than triggered. A heading with a `-` suffix
(`## [1.0.0-rc.1]`) does not match, so CI makes no prerelease; a prerelease is a `v*-*` tag pushed
by a person, which runs `release.yml` through its `push: tags` trigger. Runs of `bastionhub-ci.yml`
for a push to `main` are not cancelled by a later push; a run still pending when another push to `main` queues
is replaced, and the newer run tags its own commit when the tag does not exist.

After the GitHub Release, the `homebrew` job of `release.yml` sends a `repository_dispatch` event
(`formula-update`, payload `tool` and `tag`) to `roselabs-io/homebrew-tools`, whose workflow updates
the formula. The job uses the secret `HOMEBREW_TAP_TOKEN` and does not run for a tag containing `-`
(a prerelease).

A release is not deleted. A bad release is reverted on `main`, and a PR with a new patch heading
releases `vX.Y.Z+1`. The formula is rolled back by reverting its commit in
`roselabs-io/homebrew-tools`.

## Commands

| Recipe | Runs |
|---|---|
| `just check` | fails when `gofmt -l .` lists a file, and prints the files; `go vet ./...` for linux, darwin and windows on amd64 and arm64 |
| `just secrets` | scans every file of `git ls-files` with detect-secrets 1.5.0 against `.secrets.baseline`, offline; a finding the baseline does not hold fails |
| `just secrets update` | rewrites `.secrets.baseline` from a fresh scan, keeping the audit marks of the findings it holds |
| `just secrets-prepare` | `uv sync --script tools/secret_scan.py`: installs the scanner `just secrets` runs; needs the network, not part of the gate |
| `just test` | `go test ./...` |
| `just build` | `go build` with `CGO_ENABLED=0` for linux, darwin and windows on amd64 and arm64, into a temporary directory |
| `just gate` | `check`, `secrets`, `test`, `build`; the deterministic gate CI runs |
| `just audit` | `go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...`; needs the network, not part of the gate |
| `just fix` | `gofmt -w .` |
| `just ff` | switches to `main` and fast-forwards it from `origin`; refuses local changes or a diverged `main` |

`up`, `down` and `logs` are not applicable (`checks.recipes.not_applicable` of `pipeline.yaml`):
there is no local stack. `just`, Go and uv are required; uv runs detect-secrets for `just secrets`.
`just secrets` runs uv offline and fails until `just secrets-prepare` has installed detect-secrets;
CI runs `just secrets-prepare` before the gate. Governor's package preflight does not run it; on a
machine that runs the preflight, run `just secrets-prepare` once before `just gate`. On the first run,
`just audit` downloads govulncheck.

`bastionhub-ci.yml` runs `just gate` on pull requests and on pushes to `main` and `queue/**`. `GOVERNOR_JOBS` sets Go's package
parallelism for these recipes and defaults to `1`. The gate does not run network-dependent
vulnerability audits.

The secret scan compares findings with the baseline by file, hash and type, not by line, and does not
rewrite the baseline. It reads every tracked text file, including those detect-secrets skips by
extension such as `go.sum`; `.secrets.baseline` itself is not scanned. It finds what detect-secrets'
patterns and entropy rules find; untracked files and Git history are not scanned. To accept a finding
that is not a secret, or to refresh the baseline's line numbers, run `just secrets update` and review
the diff. `detect-secrets scan --baseline` does not record `go.sum` findings. An `audit` finding is triaged like a review finding (Finding triage).

## CI

All workflows run on GitHub-hosted `ubuntu-latest`. The repository is public; the organization's
self-hosted runners do not serve it. `validate` and `bastionhub-pm.yml` check out the private
`roselabs-io/governor` with the organization secret `GOVERNOR_PM_TOKEN`, which a workflow run from a
fork does not receive.

## Pull requests

A PR from a branch `agent/<N>-…` carries exactly one `Bastionhub-Issue: #N` line; a dispatched run
also requires a `Bastionhub-Agent` line naming a configured lane. `validate` of
`bastionhub-ci.yml` checks both. `bastionhub-pm.yml` moves issue N on the board from that line.
`autonomy: full`: the merge queue merges every approved PR; no merge waits for a person.

A change to the CLI grammar, the `endpoints.yaml` schema, a deploy artifact identifier or a far-end
`serve` route is a contract change ([Contract surface](#contract-surface)) and is recorded in
`CHANGELOG.md`.

## Finding triage

`docs.operating_model` in `pipeline.yaml` names this file. Each independent-review finding is
triaged before a correction is requested. The dispatcher runs this as a read-only triage session
(Governor `governor/docs/service/finishing.md`, Review triage); an operator correcting by hand
applies the same rules:

Only a CRITICAL finding requests changes; a review with WARNING and INFO findings only approves, and
its WARNINGs are triaged before the merge queue (Governor `governor/docs/postures/pr-review-posture.md`).

1. Open the cited code and confirm the finding against it. A finding that does not hold is answered
   on the PR with the evidence, and no correction is requested for it.
2. Request a correction for every confirmed CRITICAL and for every confirmed WARNING that is a
   boundary finding: security (including credentials, private keys, what `serve` accepts or relays,
   and what the dispatcher pushes, merges or deletes), data loss, or the issue's contract or scope.
3. Each other confirmed WARNING is either corrected or dispositioned: a PR comment names the
   follow-up issue and why deferral is safe, and the follow-up issue is opened.

Every later review of the PR receives the dispositions and does not reopen one without new evidence.
