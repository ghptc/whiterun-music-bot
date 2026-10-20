#!/usr/bin/env python3
"""Bounded native Go fuzz campaigns. Each target gets separate evidence."""
import argparse
import json
import os
from pathlib import Path
import subprocess
from qa008_support import bounded_run
import time

ROOT = Path(__file__).resolve().parents[1]
TARGETS = [
 ("media", "MediaURLs"), ("media", "RecordingMetadata"),
 ("media", "DiagnosticBudget"), ("player", "Ogg"),
 ("player", "Timestamp"), ("player", "Queue"), ("discord", "QueueArguments"),
]
parser = argparse.ArgumentParser()
parser.add_argument("--seconds", type=int, default=15)
parser.add_argument("--workers", type=int, default=2)
args = parser.parse_args()
if not 1 <= args.seconds <= 3600 or not 1 <= args.workers <= 8:
    parser.error("seconds must be 1..3600 and workers 1..8")
evidence = ROOT / "docs/qa/advanced/evidence/fuzz"
evidence.mkdir(parents=True, exist_ok=True)
summary = []
for package, name in TARGETS:
    target = "FuzzAdvanced" + name
    command = ["go", "test", "./internal/" + package, "-run=^$", "-fuzz=^" + target + "$",
               f"-fuzztime={args.seconds}s", f"-parallel={args.workers}", f"-timeout={args.seconds + 60}s"]
    env = dict(os.environ, GOMEMLIMIT="256MiB", GOMAXPROCS=str(args.workers))
    start = time.monotonic()
    try:
        result = bounded_run(command, cwd=ROOT, env=env, capture_output=True, text=True,
                                timeout=args.seconds + 90)
        output, code = result.stdout + result.stderr, result.returncode
    except subprocess.TimeoutExpired as exc:
        output, code = str(exc), 124
    (evidence / (target + ".txt")).write_text(output)
    record = dict(target=target, command=command, exit_code=code,
                  elapsed_seconds=round(time.monotonic() - start, 3),
                  workers=args.workers, gomemlimit="256MiB per process (soft runtime limit)")
    summary.append(record)
    print(json.dumps(record), flush=True)
    if code:
        print(output[-2500:], flush=True)
(evidence / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
raise SystemExit(any(row["exit_code"] for row in summary))
