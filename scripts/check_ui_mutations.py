#!/usr/bin/env python3
"""Verify V2 rendering tests reject targeted defects in disposable source copies."""
import pathlib
import shutil
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
mutants = {
    "stop-at-ten": ("pos <= end; pos++", "pos <= min(end, start+9); pos++"),
    "eleven-artworks": ("view.Shown < queueBlockSize", "view.Shown <= queueBlockSize"),
    "ignore-footer-budget": ("units+size+footerUnits > queueTextBudget", "units+size+footerUnits*0 > queueTextBudget"),
    "reduced-text-budget": ("queueTextBudget      = 4000", "queueTextBudget      = 2000"),
    "extra-followup": (
        'err = sendUpdate(view.Blocks[0].update(""), ctx)',
        '_, _ = b.client.Rest.CreateFollowupMessage(b.client.ApplicationID, e.Token(), d.MessageCreate{Content: "extra"}); err = sendUpdate(view.Blocks[0].update(""), ctx)',
    ),
    "exclusive-to": ("end = *to", "end = *to - 1"),
    "wrong-next-position": ("start+shown)", "start+shown+1)"),
    "current-consumes-preview": ("art != \"\" && row.Position > 0 && preview", "art != \"\" && preview"),
}
with tempfile.TemporaryDirectory(prefix="bard-ui-mutations-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(root / name, work / name)
    shutil.copytree(root / "internal", work / "internal")
    target = work / "internal/discord/queue.go"
    original = target.read_text()
    command = ["go", "test", "./internal/discord", "-run",
               "TestCR007|TestQueueCompactSingleMessage|TestQueueRangesAndWholeSongs|TestQueueSingleMessageDelivery",
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
    target.write_text(original)
    target = work / "internal/discord/commands.go"
    original = target.read_text()
    before = 'Name: "limit", Description: "Maximum upcoming tracks to show in one message",'
    after = before + ' Required: true,'
    if original.count(before) != 1:
        raise SystemExit("Mutation site changed: required-limit")
    target.write_text(original.replace(before, after, 1))
    result = subprocess.run(command, cwd=work, capture_output=True, text=True)
    if result.returncode == 0 or "[build failed]" in result.stdout + result.stderr:
        raise SystemExit("Mutant not killed by assertions: required-limit\n" + result.stdout + result.stderr)
    print("KILLED required-limit")
