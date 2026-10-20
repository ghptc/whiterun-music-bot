#!/usr/bin/env python3
"""Assertion-based QA-013 mutants in a disposable copy; never edit the checkout."""
import json
import pathlib
import shutil
import subprocess
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "docs/qa/command-lifecycle/evidence"
EVIDENCE.mkdir(parents=True, exist_ok=True)
TESTS = "^TestQA013Production302ShuffleEOF$|^TestQA013OrderedCommandPairs$|^TestQA013CommandAcknowledgementAndReplyFaults$"
COMMAND = ["go", "test", "./internal/player", "./internal/discord", "-run", TESTS, "-count=1", "-timeout=60s"]


def change_once(source, before, after):
    if source.count(before) != 1:
        raise RuntimeError("mutation site moved or ambiguous: " + before[:80])
    return source.replace(before, after, 1)


def missing_wakeup(source):
    start = source.index("\t// A queue mutation can expose")
    end = source.index("\n\tselect {\n\tcase p.wake", start)
    return source[:start] + source[end:]


def missing_ack_guard(source):
    start = source.index('b.log.Warn("command acknowledgement failed"')
    end = source.index("\n\t}", start)
    return source[:start] + change_once(source[start:end], "\n\t\treturn", "") + source[end:]


def stop_retains_queue(source):
    start = source.index("func (p *GuildPlayer) Stop()")
    end = source.index("\n// Leave", start)
    return source[:start] + change_once(source[start:end], "p.queue = nil", "// mutant retains upcoming queue") + source[end:]


mutants = [
    ("lost-resolver-window-wakeup", "internal/player/player.go", missing_wakeup),
    ("execute-after-failed-ack", "internal/discord/commands.go", missing_ack_guard),
    ("default-skip-two", "internal/discord/commands.go", lambda s: change_once(s, "count := 1", "count := 2")),
    ("move-loses-slot", "internal/player/controls.go", lambda s: change_once(s, "p.queue[j] = entry", "p.queue[i] = entry")),
    ("stop-retains-queue", "internal/player/player.go", stop_retains_queue),
]
summary = []
with tempfile.TemporaryDirectory(prefix="bard-qa013-mutants-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(ROOT / name, work / name)
    shutil.copytree(ROOT / "internal", work / "internal")
    original = {name: (work / name).read_text() for _, name, _ in mutants}
    baseline = subprocess.run(COMMAND, cwd=work, capture_output=True, text=True, timeout=90)
    if baseline.returncode:
        raise SystemExit("baseline failed:\n" + baseline.stdout + baseline.stderr)
    for name, target, mutate in mutants:
        path = work / target
        path.write_text(mutate(original[target]))
        start = time.monotonic()
        try:
            result = subprocess.run(COMMAND, cwd=work, capture_output=True, text=True, timeout=90)
        finally:
            path.write_text(original[target])
        output = result.stdout + result.stderr
        status = "survived" if result.returncode == 0 else "killed" if "--- FAIL: TestQA013" in output and "[build failed]" not in output else "invalid"
        row = {"mutant": name, "status": status, "exit_code": result.returncode, "elapsed_seconds": round(time.monotonic()-start, 3), "failing_tests": [line.strip() for line in output.splitlines() if "--- FAIL: TestQA013" in line][:12]}
        summary.append(row)
        print(json.dumps(row), flush=True)
(EVIDENCE / "mutations.json").write_text(json.dumps({"command": COMMAND, "baseline_passed": True, "mutants": summary}, indent=2) + "\n")
raise SystemExit(any(row["status"] != "killed" for row in summary))
