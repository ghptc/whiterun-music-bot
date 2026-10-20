#!/usr/bin/env python3
"""Summarize saved QA-013 evidence; does not execute tests or modify services."""
import argparse
import collections
import hashlib
import json
import pathlib
import re

parser = argparse.ArgumentParser()
parser.add_argument("--raw-dir", type=pathlib.Path, default=pathlib.Path(".qa-runs/qa013"))
parser.add_argument("--output", type=pathlib.Path, default=pathlib.Path("docs/qa/command-lifecycle/evidence/summary.json"))
args = parser.parse_args()


def test_summary(filename):
    records = [json.loads(line) for line in (args.raw_dir / filename).read_text().splitlines() if line.startswith("{")]
    tests = {(r["Package"], r["Test"]): r["Action"] for r in records if "Test" in r and r["Action"] in ("pass", "fail", "skip")}
    leaves = {(pkg, name): action for (pkg, name), action in tests.items() if not any(other_pkg == pkg and other.startswith(name + "/") for other_pkg, other in tests)}
    qa = collections.Counter()
    for (pkg, name), action in leaves.items():
        if name.startswith("TestQA013") and action == "pass":
            qa[name.split("/")[0]] += 1
    packages = [{"package": r["Package"], "result": r["Action"], "elapsed_seconds": r.get("Elapsed", 0)} for r in records if "Test" not in r and r["Action"] in ("pass", "fail", "skip")]
    skips = [{"package": pkg, "test": name} for (pkg, name), action in leaves.items() if action == "skip"]
    return {"packages": packages, "leaf_results": dict(collections.Counter(leaves.values())), "qa013_passed_leaves_by_contract": dict(sorted(qa.items())), "skipped": skips}


summary = {"normal": test_summary("test.jsonl"), "race": test_summary("race.jsonl"), "raw_directory": str(args.raw_dir)}
fuzz = []
for file in sorted(args.raw_dir.glob("fuzz-*.log")):
    raw = file.read_text()
    samples = re.findall(r"execs: (\d+).*?new interesting: (\d+) \(total: (\d+)\)", raw)
    fuzz.append({"target": file.stem, "passed": "\nPASS\n" in raw, "executions": int(samples[-1][0]) if samples else 0, "new_interesting": int(samples[-1][1]) if samples else 0, "corpus_total": int(samples[-1][2]) if samples else 0})
summary["fuzz"] = fuzz
source = args.raw_dir / "source-commit.txt"
if source.exists(): summary["tested_source_commit"] = source.read_text().strip()
benchmarks = collections.defaultdict(list)
for file in args.raw_dir.glob("*benchmarks.log"):
    for line in file.read_text().splitlines():
        if line.startswith("Benchmark"):
            columns = line.split()
            row = {columns[i+1]: float(columns[i]) for i in range(2, len(columns)-1, 2)}
            benchmarks[columns[0]].append(row)
summary["benchmarks"] = dict(benchmarks)
raw_coverage = (args.raw_dir / "coverage-functions.log").read_text()
summary["overall_statement_coverage"] = re.findall(r"total:.*?([\d.]+%)", raw_coverage)[-1]
summary["vet_output_empty"] = not (args.raw_dir / "vet.log").read_text().strip()
summary["storage_crash_passed"] = bool(re.search(r"^ok\s+whiterun/internal/storage", (args.raw_dir / "storage-crash.log").read_text(), re.M))
summary["stress_passed"] = bool(re.search(r"^ok\s+whiterun/internal/player", (args.raw_dir / "stress.log").read_text(), re.M))
build = (args.raw_dir / "docker-build.log").read_text()
summary["docker_build_passed"] = "Successfully built" in build or "exporting to image" in build
summary["docker_image_id"] = (re.findall(r"Successfully built (\w+)", build) or [None])[-1]
summary["raw_sha256"] = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(args.raw_dir.iterdir()) if p.is_file()}
args.output.parent.mkdir(parents=True, exist_ok=True)
args.output.write_text(json.dumps(summary, indent=2) + "\n")
print(json.dumps({"normal": summary["normal"]["leaf_results"], "race": summary["race"]["leaf_results"], "fuzz_executions": sum(r["executions"] for r in fuzz), "coverage": summary["overall_statement_coverage"]}))
