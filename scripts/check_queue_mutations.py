#!/usr/bin/env python3
"""Run targeted queue mutants in disposable copies; never modify the checkout."""
import pathlib
import shutil
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
mutants = {
    "remove-wrong-position": ("i := position - 1", "i := 0"),
    "move-loses-entry": ("p.queue[j] = entry", "p.queue[i] = entry"),
    "shuffle-duplicates-entry": (
        "p.queue[i], p.queue[j] = p.queue[j], p.queue[i]",
        "p.queue[i], p.queue[j] = p.queue[j], p.queue[j]",
    ),
    "jump-off-by-one": (
        "p.queue = p.queue[position-1:]", "p.queue = p.queue[position:]"
    ),
}
with tempfile.TemporaryDirectory(prefix="bard-mutations-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(root / name, work / name)
    shutil.copytree(root / "internal", work / "internal")
    target = work / "internal/player/controls.go"
    original = target.read_text()
    command = ["go", "test", "./internal/player", "-run",
               "TestQueueEditsModelAndMutation|TestPendingEditsKeepResolverIdentity",
               "-count=1", "-timeout=15s"]
    baseline = subprocess.run(command, cwd=work, capture_output=True, text=True)
    if baseline.returncode:
        raise SystemExit("Baseline failed:\n" + baseline.stdout + baseline.stderr)
    for name, (before, after) in mutants.items():
        if original.count(before) != 1:
            raise SystemExit(f"Mutation site changed: {name}")
        target.write_text(original.replace(before, after, 1))
        result = subprocess.run(command, cwd=work, capture_output=True, text=True)
        if result.returncode == 0 or "[build failed]" in result.stdout + result.stderr:
            raise SystemExit(f"Mutant not killed by assertions: {name}\n{result.stdout}{result.stderr}")
        print(f"KILLED {name}")
