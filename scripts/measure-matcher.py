#!/usr/bin/env python3
"""Measure one compiled Go fixture benchmark, without compilation or network."""
import argparse
import json
import resource
import subprocess
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("binary", help="absolute path from go test -c")
parser.add_argument("--package-dir", default="internal/media")
args = parser.parse_args()
started = time.monotonic()
before = resource.getrusage(resource.RUSAGE_CHILDREN)
run = subprocess.run(
    [args.binary, "-test.run=^$", "-test.bench=BenchmarkFastMatchRealFixture",
     "-test.benchtime=10000x"],
    cwd=args.package_dir, capture_output=True, text=True, check=True,
)
after = resource.getrusage(resource.RUSAGE_CHILDREN)
print(json.dumps({
    "wall_seconds": time.monotonic() - started,
    "user_seconds": after.ru_utime - before.ru_utime,
    "system_seconds": after.ru_stime - before.ru_stime,
    "max_rss_kib": after.ru_maxrss,  # Linux units; includes Go runtime/test harness.
    "benchmark": run.stdout,
}, indent=2))
