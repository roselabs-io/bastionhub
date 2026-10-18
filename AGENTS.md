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
| `.github/workflows/release.yml` | on a `v*` tag: builds six targets, archives, `checksums.txt`, a GitHub Release |
| `.github/workflows/bastionhub-ci.yml` | `validate` (PR metadata) and `gate` (`just gate`) |
| `.github/workflows/bastionhub-pm.yml` | the issue and board sync of Governor |
| `pipeline.yaml` | the Governor dispatcher's config for this repository |
| `tools/local_preflight.py` | the local check the dispatcher runs: `just check`, then `just gate` |

A merge into `main` deploys nothing. A release is a `v*` tag; the Homebrew formula in
`roselabs-io/homebrew-tools` is updated by hand (bastionhub#4).

## Commands

| Recipe | Runs |
|---|---|
| `just check` | `go vet ./...`; fails when `gofmt -l .` lists a file |
| `just test` | `go test ./...` |
| `just build` | `go build` with `CGO_ENABLED=0` for linux, darwin and windows on amd64 and arm64, into a temporary directory |
| `just gate` | `check`, `test`, `build`; the deterministic gate CI runs |
| `just fix` | `gofmt -w .` |
| `just ff` | switches to `main` and fast-forwards it from `origin`; refuses local changes or a diverged `main` |

`up`, `down` and `logs` are not applicable (`checks.recipes.not_applicable` of `pipeline.yaml`):
there is no local stack. `just` and Go are required.

`go vet` in the gate runs for the host target only; `ci.yml` vets each of the six targets. The gate
does not scan for secrets and there is no `audit` recipe (bastionhub#2).

## CI

All workflows run on GitHub-hosted `ubuntu-latest`. The repository is public; the organization's
self-hosted runners do not serve it. `validate` and `bastionhub-pm.yml` check out the private
`roselabs-io/governor` with the organization secret `GOVERNOR_PM_TOKEN`, which a workflow run from a
fork does not receive.

## Pull requests

A PR from a branch `agent/<N>-…` carries exactly one `Bastionhub-Issue: #N` line; a dispatched run
also requires a `Bastionhub-Agent` line naming a configured lane. `validate` of
`bastionhub-ci.yml` checks both. `bastionhub-pm.yml` moves issue N on the board from that line.
`autonomy: gated`: every merge is left to a person.

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
