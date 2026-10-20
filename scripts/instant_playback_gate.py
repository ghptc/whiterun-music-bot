#!/usr/bin/env python3
"""Run or evaluate PERF-017 evidence. External observations never approve Voice/DAVE."""
import argparse
import collections
import json
import hashlib
import math
import os
import pathlib
import platform
import subprocess
import sys
import time

BASE_SCENARIOS = ["spotify-single", "youtube-single", "spotify-album", "playlist-10",
                  "playlist-50", "playlist-123", "playlist-302", "playlist-500",
                  "youtube-force", "voice-disconnected", "after-restart",
                  "after-shuffle", "progressive-active"]
EXPECTED = {(name, mode) for name in BASE_SCENARIOS for mode in ("cold", "warm")}
EXPECTED |= {(f"load-{n}-guilds", "warm") for n in (2, 5, 10)}
EXPECTED.add(("ready-transition", "ready"))
STAGES = {"received", "ack", "metadata", "first_resolved", "extraction", "ffmpeg_started", "opus", "transport_sent"}


def distribution(samples):
    ordered = sorted(samples)
    if not ordered:
        return {"n": 0}
    return {"n": len(ordered), **{name: ordered[math.ceil(q * len(ordered)) - 1] / 1e6
                                   for name, q in [("p50_ms", .5), ("p95_ms", .95),
                                                   ("p99_ms", .99), ("max_ms", 1)]}}


def read_evidence(path):
    buffers = collections.defaultdict(str)
    failures = []
    for line in pathlib.Path(path).read_text().splitlines(keepends=True):
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            buffers["plain"] += line  # Go test binary -test.v output
            continue
        if row.get("Action") == "fail":
            failures.append(row.get("Test", row.get("Package", "unknown")))
        buffers[row.get("Test", "")] += row.get("Output", "")
    records = []
    # test2json fragments very long t.Log lines; join by test before parsing.
    for text in buffers.values():
        for line in text.splitlines():
            if "PERF_GATE_JSON " in line:
                records.append(json.loads(line.split("PERF_GATE_JSON ", 1)[1]))
    return records, failures


def evaluate(records, kind, failures=()):
    errors = list(failures)
    found = set()
    summaries = []
    for record in records:
        scenario = record["scenario"]
        key = (scenario["Name"], scenario["Mode"])
        if key in found:
            errors.append(f"duplicate scenario: {key}")
        found.add(key)
        environment = record.get("environment")
        expected_env = ("deterministic-dispatch-real-ffmpeg-controlled-ytdlp-simulated-udp"
                        if kind == "deterministic" else "real-providers-discord-dave")
        row_errors = []
        if environment != expected_env:
            row_errors.append(f"environment {environment!r} cannot approve {kind}")
        samples = record.get("monotonic_ns") or []
        stages = ({"ready_eof", "extraction", "ffmpeg_started", "opus", "transport_sent"}
                  if scenario.get("Transition") else STAGES)
        durations = []
        for sample in samples:
            if not stages.issubset(sample):
                row_errors.append("incomplete stage timestamps")
                continue
            origin = sample.get("received", sample.get("ready_eof", 0))
            duration = sample["transport_sent"] - origin
            if duration <= 0 or sample["transport_sent"] < sample["opus"] or any(sample[stage] < origin or sample[stage] > sample["transport_sent"] for stage in stages):
                row_errors.append("invalid monotonic frame timestamps")
            durations.append(duration)
        stats = distribution(durations)
        budget = 1000 if scenario.get("Transition") else (2000 if scenario["Mode"] == "warm" else 5000)
        if stats["n"] < 100:
            row_errors.append(f"only {stats['n']} samples; require >=100")
        elif stats["p95_ms"] > budget:
            row_errors.append(f"p95 {stats['p95_ms']:.3f}ms > {budget}ms")
        guilds=scenario.get("Guilds",1)
        if guilds>1:
            for guild_index in range(1,guilds+1):
                guild_samples=[s["transport_sent"]-s.get("received",0) for s in samples if s.get("guild_index")==guild_index]
                guild_stats=distribution(guild_samples)
                if guild_stats["n"]<100 or guild_stats.get("p95_ms",float("inf"))>budget:
                    row_errors.append(f"guild {guild_index}: insufficient or over-budget first frames")
        if record.get("errors"):
            row_errors.append("provider/transport failures occurred")
        if record.get("status") != "PASS":
            row_errors.append(f"producer status {record.get('status')}")
        if kind=="real" and record.get("ack_transport")!="discord-rest":
            row_errors.append("interaction acknowledgement was not measured over Discord REST")
        if kind == "real" and not record.get("dave_verified"):
            row_errors.append("missing verified DAVE transport attestation")
        summary = {"scenario": scenario, "ttfa": stats, "budget_ms": budget,
                   "environment": environment, "status": "FAIL" if row_errors else "PASS",
                   "errors": sorted(set(row_errors)), "resources": record.get("resources"),
                   "cpu_ms": record.get("cpu_ms"), "peak_sampled_heap_bytes": record.get("peak_sampled_heap_bytes"),
                   "peak_sampled_goroutines": record.get("peak_sampled_goroutines")}
        summaries.append(summary)
        errors.extend(f"{key}: {error}" for error in summary["errors"])
    missing = EXPECTED - found
    errors.extend(f"missing scenario: {key}" for key in sorted(missing))
    by_key = {(r["scenario"]["Name"], r["scenario"]["Mode"]): r for r in summaries}
    growth = None
    small, large = by_key.get(("playlist-10", "warm")), by_key.get(("playlist-500", "warm"))
    if small and large and small["ttfa"]["n"] >= 100 and large["ttfa"]["n"] >= 100:
        growth = large["ttfa"]["p95_ms"] - small["ttfa"]["p95_ms"]
        if growth > 250:
            errors.append(f"playlist scaling regression: +{growth:.3f}ms exceeds +250ms")
    return {"kind": kind, "status": "FAIL" if errors else "PASS", "release_approved": kind == "real" and not errors,
            "errors": errors, "scenarios": summaries, "playlist_10_to_500_p95_growth_ms": growth,
            "scaling_budget_ms": 250}


def version(command):
    try:
        return subprocess.check_output(command, stderr=subprocess.STDOUT, text=True).splitlines()[0]
    except (OSError, subprocess.CalledProcessError):
        return "unavailable"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="run the isolated deterministic gate")
    parser.add_argument("--input", type=pathlib.Path)
    parser.add_argument("--kind", choices=["deterministic", "real"], default="deterministic")
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if args.run:
        if args.kind != "deterministic":
            parser.error("real homologation consumes explicitly authorized Discord/DAVE evidence")
        args.input = args.output.with_suffix(".raw.jsonl")
        args.input.parent.mkdir(parents=True, exist_ok=True)
        env = dict(os.environ, BARD_INSTANT_GATE="1", BARD_GATE_SAMPLES="100")
        with args.input.open("w") as output:
            process = subprocess.run(["go", "test", "./internal/discord", "-run",
                                      "^TestInstantPlayback(Gate|TransitionGate)$", "-count=1", "-v", "-json", "-timeout=10m"],
                                     env=env, stdout=output, stderr=subprocess.STDOUT)
        exit_code = process.returncode
    else:
        if not args.input:
            parser.error("--input or --run is required")
        exit_code = 0
    try:
        records, failures = read_evidence(args.input)
    except (OSError, ValueError) as error:
        records,failures=[],["evidence unavailable or invalid: "+str(error)]
    report = evaluate(records, args.kind, failures)
    if exit_code:
        report["errors"].append(f"go test exited {exit_code}")
        report["status"] = "FAIL"
    cpu = "unknown"
    if pathlib.Path("/proc/cpuinfo").exists():
        cpu = next((line.partition(":")[2].strip() for line in pathlib.Path("/proc/cpuinfo").read_text().splitlines()
                    if line.startswith("model name")), cpu)
    source=hashlib.sha256()
    for path in sorted(pathlib.Path("internal").rglob("*.go")):
        source.update(str(path).encode());source.update(path.read_bytes())
    report["configuration"]={"minimum_samples_per_scenario":100,"match_slots":4,"background_slots":3,
                              "collection_workers":3,"logical_prefetch":6,"search_admission_slots":4,
                              "search_admission_deadline_ms":5000,"source_go_sha256":source.hexdigest(),
                              "quantiles":"nearest-rank","external_network":args.kind=="real"}
    report["host"] = {"platform": platform.platform(), "cpu": cpu, "logical_cpus": os.cpu_count(),
                      "go": version(["go", "version"]), "ffmpeg": version(["ffmpeg", "-version"]),
                      "monotonic_resolution_seconds": time.get_clock_info("monotonic").resolution,
                      "gomaxprocs": os.environ.get("GOMAXPROCS", "Go default"),
                      "revision": version(["git", "rev-parse", "HEAD"])}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    for row in report["scenarios"]:
        scenario, stats = row["scenario"], row["ttfa"]
        print(f"{scenario['Name']}/{scenario['Mode']}: {row['status']} n={stats['n']} "
              f"p50={stats.get('p50_ms', 0):.3f} p95={stats.get('p95_ms', 0):.3f} "
              f"p99={stats.get('p99_ms', 0):.3f} max={stats.get('max_ms', 0):.3f} ms")
    print(f"{args.kind.upper()}: {report['status']}; Discord release approved={report['release_approved']}")
    if report["errors"]:
        print("\n".join(report["errors"]), file=sys.stderr)
    return 0 if report["status"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
