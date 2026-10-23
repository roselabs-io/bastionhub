"""Scan tracked files for secrets not listed in the baseline.

Run with ``uv run --with detect-secrets==1.5.0 python tools/secret_scan.py``.
The scan compares file, secret hash and type; baseline line numbers may become stale.
It does not scan untracked files or Git history. Detection is limited to the
patterns and entropy rules supplied by detect-secrets.
"""

from __future__ import annotations

import subprocess
import sys

from detect_secrets import SecretsCollection
from detect_secrets.core import baseline
from detect_secrets.exceptions import UnableToReadBaselineError
from detect_secrets.settings import transient_settings

BASELINE = ".secrets.baseline"


def findings() -> list[str]:
    """Return new findings as ``path:line: type`` records."""
    tracked = subprocess.run(["git", "ls-files", "-z"], capture_output=True, text=True, check=True).stdout
    with transient_settings({}):
        accepted = baseline.load(baseline.load_from_file(BASELINE), BASELINE)
        scanned = SecretsCollection()
        scanned.scan_files(*filter(None, tracked.split("\0")))
        new = scanned - accepted
    return sorted(f"{path}:{secret.line_number}: {secret.type}" for path, secret in new)


def main() -> int:
    try:
        found = findings()
    except UnableToReadBaselineError:
        print(f"{BASELINE}: missing or not JSON", file=sys.stderr)
        return 2
    for finding in found:
        print(finding)
    if found:
        print(f"{len(found)} secrets not in {BASELINE}", file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    raise SystemExit(main())
