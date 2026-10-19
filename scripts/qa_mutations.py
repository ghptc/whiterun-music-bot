#!/usr/bin/env python3
"""Behavioral mutants run only in disposable source copies, never production data."""
import json
import pathlib
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
# Each targeted command first passes unmodified. Build errors and timeout are
# inconclusive, never credited as detected behavior.
MUTANTS = [
 ("voice-validation", "internal/discord/commands.go",
  "if !ok || state.ChannelID == nil {", "if false && (!ok || state.ChannelID == nil) {",
  "./internal/discord", "TestQualityVoiceBoundary"),
 ("queue-order", "internal/player/controls.go",
  "p.queue[j] = entry", "p.queue[i] = entry",
  "./internal/player", "TestQualityQueueModel"),
 ("capacity", "internal/player/player.go",
  "len(tracks) > MaxQueueTracks-len(p.queue)", "len(tracks) > MaxQueueTracks+1-len(p.queue)",
  "./internal/player", "TestQualityTwentyProducers"),
 ("cache-expiry", "internal/media/cache.go",
  "!ok || !now.Before(entry.expires)", "!ok",
  "./internal/media", "TestQualityCacheBoundaries"),
 ("cache-alias", "internal/media/cache.go",
  "return CloneTrack(entry.track), true", "return entry.track, true",
  "./internal/media", "TestQualityCacheBoundaries"),
 ("isrc-contradiction", "internal/media/spotify_rank.go",
  'return reject("isrc_mismatch")', '// injected: ignore conflicting ISRC',
  "./internal/media", "TestQualityIdentityMatrix"),
 ("version-contradiction", "internal/media/spotify_rank.go",
  'versionPresent(intended, marker) != versionPresent(versionText+" "+c.Album, marker)',
  'false && versionPresent(intended, marker) != versionPresent(versionText+" "+c.Album, marker)',
  "./internal/media", "TestQualityIdentityMatrix"),
 ("history-restart", "internal/storage/sqlite.go",
  "err = json.Unmarshal([]byte(b), &state)", "err = json.Unmarshal([]byte(b), &state)\nstate.History = nil",
  "./internal/storage", "TestQualitySQLiteRestart"),
 ("mapping-invalidation", "internal/storage/sqlite.go",
  '"DELETE FROM mappings WHERE identity=?"', '"DELETE FROM mappings WHERE identity=? AND 0"',
  "./internal/storage", "TestSQLiteReopenAndMappingInvalidation"),
]
results = []
with tempfile.TemporaryDirectory(prefix="bard-quality-mutation-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(ROOT / name, work / name)
    shutil.copytree(ROOT / "internal", work / "internal")
    for name, file, before, after, package, pattern in MUTANTS:
        target = work / file
        original = target.read_text()
        if original.count(before) != 1:
            raise SystemExit(f"Mutation site changed: {name}")
        command = ["go", "test", package, "-run", pattern, "-count=1", "-timeout=30s"]
        baseline = subprocess.run(command, cwd=work, capture_output=True, text=True, timeout=90)
        if baseline.returncode:
            raise SystemExit(f"Baseline failed: {name}\n{baseline.stdout}{baseline.stderr}")
        target.write_text(original.replace(before, after, 1))
        try:
            result = subprocess.run(command, cwd=work, capture_output=True, text=True, timeout=90)
            output = result.stdout + result.stderr
            if result.returncode == 0:
                status = "SURVIVED"
            elif "[build failed]" in output or "panic: test timed out" in output:
                status = "INCONCLUSIVE"
            elif "--- FAIL:" in output:
                status = "DETECTED"
            else:
                status = "INCONCLUSIVE"
            results.append(dict(name=name, status=status, command=command, output=output))
            print(f"{status} {name}", flush=True)
        except subprocess.TimeoutExpired:
            results.append(dict(name=name, status="INCONCLUSIVE", output="runner timeout"))
        finally:
            target.write_text(original)
destination = ROOT / "docs/qa/evidence/mutations.json"
destination.parent.mkdir(parents=True, exist_ok=True)
destination.write_text(json.dumps(results, indent=2) + "\n")
raise SystemExit(any(r["status"] != "DETECTED" for r in results))
