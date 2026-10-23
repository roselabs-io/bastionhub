# AGENTS.md

Instructions for agent sessions working in this repository. `pipeline.yaml` names this file for
`docs.agents`, `docs.contract`, `docs.review_posture` and `docs.operating_model`.

[`CLAUDE.md`](CLAUDE.md) holds the scope, the contract surface (CLI grammar, `endpoints.yaml`,
deploy artifact identifiers, `serve` routes), the constraints and the conventions. This file does
not repeat them.

## Repository

A Go CLI (`go.mod`: `go 1.25.5`; dependencies `urfave/cli/v3` and `gopkg.in/yaml.v3`). Certificate
operations shell out to `sshca` at runtime. `bastionhub serve` also runs on the bastion as the
systemd unit `bastionhub-serve.service`.

| Path | Content |
|---|---|
| `main.go` | the CLI; `version` is a `var` set with `-ldflags "-X main.version=<tag>"` at release |
| `serve.go` | `bastionhub serve` (the invite and relay service) and `bastionhub invite` |
| `main_test.go`, `serve_test.go` | the tests (`go test ./...`) |
| `deploy/install.sh` | the bastion installer for Debian and Ubuntu |
| `deploy/bastion/` | sshd drop-ins and `principal-to-acl.sh` installed on the bastion; `README.md` describes them |
| `README.md`, `CHANGELOG.md`, `CLAUDE.md` | usage, release notes, contributor notes |
| `.github/workflows/ci.yml` | `go vet` and `go build` for six targets, `go test` on linux/amd64 |
| `.github/workflows/release.yml` | on a `v*` tag: builds six targets, archives, `checksums.txt`, a GitHub Release, then a `formula-update` event to `roselabs-io/homebrew-tools` |
| `.github/workflows/bastionhub-ci.yml` | `validate` (PR metadata) and `gate` (`just gate`) |
| `.github/workflows/audit.yml` | `just audit` on a change to `go.mod` or `go.sum`, and weekly on `main` |
| `.secrets.baseline` | the detect-secrets baseline `just secrets` reads: findings that are not secrets |
| `.github/workflows/bastionhub-pm.yml` | the issue and board sync of Governor |
| `pipeline.yaml` | the Governor dispatcher's config for this repository |
| `tools/secret_scan.py` | the secret scan `just secrets` runs |
| Governor's package preflight (`tools/local_preflight.py` of the Governor package, run by the dispatcher; this repository keeps no copy) | the local check the dispatcher runs: `just check`, then `just gate` |

A merge into `main` deploys nothing. A release is a `v*` tag. After the GitHub Release, the
`homebrew` job of `release.yml` sends a `repository_dispatch` event (`formula-update`, payload
`tool` and `tag`) to `roselabs-io/homebrew-tools`, whose workflow updates the formula. The job uses
the secret `HOMEBREW_TAP_TOKEN` and does not run for a tag containing `-` (a prerelease).

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

`just gate` runs the checks of `ci.yml` plus the format check and the secret scan; `ci.yml` runs
`go test` on linux/amd64 only, the gate on the host.

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
`autonomy: tiered`: the merge queue merges an approved PR, except that the merge of a complex or
exceptional ticket's PR is left to a person.

A change to the CLI grammar, the `endpoints.yaml` schema, a deploy artifact identifier or a far-end
`serve` route is a contract change (`CLAUDE.md`, Contract surface) and is recorded in
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
