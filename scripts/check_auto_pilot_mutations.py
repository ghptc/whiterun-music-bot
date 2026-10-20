#!/usr/bin/env python3
"""Reject targeted fidelity regressions using real Auto Pilot metadata captures."""
import pathlib
import shutil
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
mutants = [
    ("album-suffix-not-normalized", "spotify_rank.go",
     's = albumVersionSuffix.ReplaceAllString(s, "")', 's = s'),
    ("ignore-duration-mismatch", "spotify_rank.go",
     'if delta > math.Max(', 'if false && delta > math.Max('),
    ("discard-first-confirmed", "spotify_rank.go",
     'return full, nil\n\t\tcase MatchUncertain:',
     'sawUncertain = true\n\t\tcase MatchUncertain:'),
    ("unstable-resolution-cache-key", "resolver.go",
     'key = "spotify:" + MappingKey(*r.Match, r.Policy)',
     'key = fmt.Sprint(time.Now().UnixNano())'),
]
with tempfile.TemporaryDirectory(prefix="bard-auto-pilot-mutations-") as temporary:
    work = pathlib.Path(temporary)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(root / name, work / name)
    shutil.copytree(root / "internal", work / "internal")
    command = ["go", "test", "./internal/media", "-run", "TestAutoPilot",
               "-count=1", "-timeout=15s"]
    baseline = subprocess.run(command, cwd=work, capture_output=True, text=True)
    if baseline.returncode:
        raise SystemExit("Baseline failed:\n" + baseline.stdout + baseline.stderr)
    for name, file, before, after in mutants:
        target = work / "internal/media" / file
        original = target.read_text()
        if original.count(before) != 1:
            raise SystemExit(f"Mutation site changed: {name}")
        target.write_text(original.replace(before, after, 1))
        result = subprocess.run(command, cwd=work, capture_output=True, text=True)
        target.write_text(original)
        if result.returncode == 0 or "[build failed]" in result.stdout + result.stderr:
            raise SystemExit(f"Mutant not killed by assertions: {name}\n{result.stdout}{result.stderr}")
        print(f"KILLED {name}")
