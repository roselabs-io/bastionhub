# Decisions (ADRs)

Architecture Decision Records for `bastionhub`. One file per decision, numbered, chronological. Same conventions as the upstream `gateway` repo.

## Conventions

**Filename:** `ADR-NNN-short-kebab-case-description.md` (3-digit zero-padded, monotonic).

**ADR numbering is local to this repo.** `bastionhub` starts its own ADR-001 from scratch. ADRs in the upstream `gateway` repo that informed bastionhub's existence are pointed to from [inherited.md](inherited.md), not duplicated or renumbered here.

**Status terms:**

| Status | Meaning |
|---|---|
| `Proposed` | Decision drafted, not yet committed |
| `Accepted` | Decision agreed to but not yet implemented |
| `Accepted — Implemented YYYY-MM-DD` | Decision live in the code |
| `Revised` | Original decision modified; ADR still documents the journey |
| `Superseded by ADR-NNN` | Replaced by a later ADR. Don't delete; link |

**Structure:**

```markdown
# ADR-NNN: Title

**Status:** ...

<1-3 paragraph synthesis>

**Decision:** <crisp callout if needed>

**Why:** <reasoning, alternatives>

**Constraint discovered** (optional): <gotchas from deploy>

**References:** <cross-links>
```

## What goes in an ADR vs not

- **Yes:** non-obvious architectural choices, scope changes, alternatives evaluated, constraints discovered during deploy.
- **No:** day-to-day implementation choices, code style.

## Index

| # | Title | Status |
|---|---|---|
| _none yet_ | — | — |

First bastionhub-series ADR lands when bastionhub makes its first non-inherited decision (likely a config-schema change or platform expansion).

## Inherited from upstream

See [inherited.md](inherited.md) for ADRs in the `gateway` repo that informed bastionhub's existence and shape. They are not renumbered into this repo's ADR series.
