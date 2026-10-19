#!/usr/bin/env python3
"""Verify V2 rendering tests reject targeted defects in disposable source copies."""
import pathlib
import shutil
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
mutants = {
    "artwork-always-fallback": ('URL: art', 'URL: fallbackArtwork'),
    "truncate-after-first-block": (
        'offset < len(rows); offset += queueBlockSize',
        'offset < min(len(rows), queueBlockSize); offset += queueBlockSize',
    ),
    "wrong-queue-number": ('fmt.Sprintf("%d. ", i+1)', 'fmt.Sprintf("%d. ", i+2)'),
    "leak-ready-state": (
        'text := "**" + prefix + name + "**"',
        'text := "**" + prefix + name + "**\\nready"',
    ),
    "youtube-overwrites-spotify-cover": (
        'art := media.ArtworkURL(t)',
        'art := media.SafeArtworkURL(t.YouTubeThumbnailURL)',
    ),
}
with tempfile.TemporaryDirectory(prefix="bard-ui-mutations-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(root / name, work / name)
    shutil.copytree(root / "internal", work / "internal")
    target = work / "internal/discord/queue.go"
    original = target.read_text()
    command = ["go", "test", "./internal/discord", "-run",
               "TestQueueCompleteAndLimitedSnapshots|TestQueueCurrentPendingAndArtwork|TestNowPlayingComponentsArtworkAndNativeLimits",
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
