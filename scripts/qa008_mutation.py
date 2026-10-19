#!/usr/bin/env python3
"""Small systematic campaigns with Gremlins 0.6.0, always in disposable copies."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--tool", default="/tmp/bard-qa008-tools/gremlins")
parser.add_argument("--campaign", choices=["player", "fidelity", "storage", "permissions", "all"], default="all")
args = parser.parse_args()
go = shutil.which("go")
if not go or not Path(args.tool).is_file():
    parser.error("install Gremlins v0.6.0 and provide --tool")
targets = {
    "player": ("player", ["controls.go", "queue.go", "previous.go", "durable.go"], "boundary"),
    "fidelity": ("media", ["spotify_rank.go", "fidelity.go"], "negation"),
    "storage": ("storage", ["sqlite.go"], "negation"),
    "permissions": ("discord", ["commands.go"], "negation"),
}
evidence = ROOT / "docs/qa/advanced/evidence/mutation"
evidence.mkdir(parents=True, exist_ok=True)
failed = False
for name, (package, files, operator) in targets.items():
    if args.campaign not in ("all", name):
        continue
    with tempfile.TemporaryDirectory(prefix="bard-qa008-mutation-") as temporary:
        work = Path(temporary)
        for source in ("go.mod", "go.sum"):
            shutil.copy2(ROOT / source, work / source)
        shutil.copytree(ROOT / "internal", work / "internal")
        # Some cached dependencies lack proxy metadata. Local replacements are
        # confined to this copy and preserve the exact cached source versions.
        mod = (work / "go.mod").read_text()
        module_cache = Path(subprocess.check_output([go, "env", "GOMODCACHE"], text=True).strip())
        replacements = []
        for module, version in re.findall(r"^\s*([\w./-]+) (v[^\s]+)", mod, re.M):
            escaped = "".join("!" + c.lower() if c.isupper() else c for c in module)
            cached = module_cache / (escaped + "@" + version)
            if cached.is_dir():
                replacements.append(f"replace {module} => {cached}")
        (work / "go.mod").write_text(mod + "\n" + "\n".join(replacements) + "\n")
        wrapper_dir = work / "qa-bin"
        wrapper_dir.mkdir()
        wrapper = wrapper_dir / "go"
        # Select bounded, relevant tests but leave source and tests untouched.
        wrapper.write_text('#!/bin/sh\nif [ "$1" = test ]; then\n'
                           f' exec "{go}" "$@" "-run=^(TestAdvanced|TestQuality|TestForce|TestCommandsRequireGuildAndVoice)" -timeout=20s\n'
                           f'fi\nexec "{go}" "$@"\n')
        wrapper.chmod(0o700)
        env = dict(os.environ, PATH=str(wrapper_dir) + os.pathsep + os.environ["PATH"],
                   BARD_QA_MODEL_SEED="41", GOMAXPROCS="2", GOMEMLIMIT="512MiB")
        report = work / "mutation.json"
        command = [str(Path(args.tool).resolve()), "unleash", "./internal/" + package,
                   "--output", str(report), "--workers=2", "--test-cpu=2",
                   "--timeout-coefficient=50", "--arithmetic-base=false",
                   "--increment-decrement=false", "--invert-negatives=false",
                   "--conditionals-boundary=" + str(operator == "boundary").lower(),
                   "--conditionals-negation=" + str(operator == "negation").lower()]
        for source in sorted((work / "internal" / package).glob("*.go")):
            if source.name not in files and not source.name.endswith("_test.go"):
                command += ["--exclude-files", re.escape(source.name) + "$"]
        start = time.monotonic()
        try:
            result = subprocess.run(command, cwd=work, env=env, capture_output=True,
                                    text=True, timeout=900)
            output, code = result.stdout + result.stderr, result.returncode
        except subprocess.TimeoutExpired as exc:
            output, code = str(exc), 124
        (evidence / (name + ".txt")).write_text(output)
        metadata = dict(campaign=name, package=package, files=files, operator=operator,
                        command=command, exit_code=code, elapsed_seconds=time.monotonic()-start,
                        model_seed=41, source_isolation="temporary copy",
                        module_replacements=replacements)
        (evidence / (name + "-run.json")).write_text(json.dumps(metadata, indent=2)+"\n")
        if report.exists():
            shutil.copy2(report, evidence / (name + ".json"))
            counts = {}
            for source in json.loads(report.read_text()).get("files", []):
                for mutant in source.get("mutations", []):
                    status = mutant["status"]
                    counts[status] = counts.get(status, 0) + 1
            metadata["statuses"] = counts
            # Never count timeout, compile error or missing coverage as killed.
            failed = failed or bool(counts.get("LIVED") or counts.get("TIMED OUT") or counts.get("NOT VIABLE"))
            (evidence / (name + "-run.json")).write_text(json.dumps(metadata, indent=2)+"\n")
        else:
            failed = True
        failed = failed or bool(code)
        print(json.dumps({k:v for k,v in metadata.items() if k not in ("command", "module_replacements")}), flush=True)
        if code:
            print(output[-1200:], flush=True)

raise SystemExit(1 if failed else 0)
