#!/usr/bin/env -S uv run --offline --quiet --script
# /// script
# requires-python = ">=3.9"
# dependencies = ["detect-secrets==1.5.0"]
# ///
"""The secret scan of `just secrets`: every file of `git ls-files`, against `.secrets.baseline`.

Without an argument, prints each finding the baseline does not hold (compared by file, hash and
type) as `path:line: type` and exits 1 when there is one; the baseline is only read. With `update`,
rewrites the baseline from a fresh scan, keeping the audit marks of the findings it already holds.

detect-secrets' `is_non_text_file` filter skips files by extension (`go.sum` among them) without
reading them; the scan removes it. Files are scanned one by one in this process, because the worker
processes of `SecretsCollection.scan_files` reload the default filters.
"""

import subprocess
import sys

from detect_secrets import SecretsCollection
from detect_secrets.core import baseline
from detect_secrets.settings import get_settings, transient_settings

BASELINE = ".secrets.baseline"


def main() -> int:
    tracked = subprocess.run(["git", "ls-files", "-z"], capture_output=True, text=True, check=True).stdout
    with transient_settings({}):
        accepted = baseline.load(baseline.load_from_file(BASELINE), BASELINE)
        get_settings().disable_filters("detect_secrets.filters.heuristic.is_non_text_file")
        scanned = SecretsCollection()
        for path in filter(None, tracked.split("\0")):
            scanned.scan_file(path)
        if sys.argv[1:] == ["update"]:
            scanned.merge(accepted)
            baseline.save_to_file(scanned, BASELINE)
            return 0
        found = sorted(f"{path}:{secret.line_number}: {secret.type}" for path, secret in scanned - accepted)
    for finding in found:
        print(finding)
    if found:
        print(f"{len(found)} secrets not in {BASELINE}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
