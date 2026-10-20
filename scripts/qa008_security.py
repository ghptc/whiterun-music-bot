#!/usr/bin/env python3
"""Record govulncheck output without treating missing tools/network as success."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
from qa008_support import bounded_run

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--tool", default=os.environ.get("GOVULNCHECK", "govulncheck"))
args = parser.parse_args()
evidence = ROOT / "docs/qa/advanced/evidence/security"
evidence.mkdir(parents=True, exist_ok=True)
tool = shutil.which(args.tool)
if not tool:
    status = {"status":"BLOCKED", "reason":"govulncheck unavailable; installation failed due DNS resolution",
              "attempted_command":[args.tool,"-json","./..."],"vulnerabilities":"UNKNOWN"}
    (evidence/"status.json").write_text(json.dumps(status,indent=2)+"\n")
    print(json.dumps(status))
    raise SystemExit(2)
version = bounded_run([tool,"-version"],capture_output=True,text=True,timeout=30)
(evidence/"version.txt").write_text(version.stdout+version.stderr)
try:
    run = bounded_run([tool,"-json","./..."],cwd=ROOT,capture_output=True,text=True,timeout=300)
    output, code = run.stdout+run.stderr, run.returncode
except subprocess.TimeoutExpired as exc:
    output, code = str(exc), 124
(evidence/"govulncheck.jsonl").write_text(output)
# JSON findings must be investigated: exit code zero alone in JSON mode is not
# a finding count, and an incomplete/no-progress stream is not a clean scan.
messages = []
decoder = json.JSONDecoder()
remaining = output
while remaining.strip():
    try:
        value, end = decoder.raw_decode(remaining.lstrip())
        messages.append(value)
        remaining = remaining.lstrip()[end:]
    except json.JSONDecodeError:
        break
findings = [m["finding"] for m in messages if "finding" in m]
complete = code in (0,3) and not remaining.strip() and any("progress" in m for m in messages)
sbom = next((m["SBOM"] for m in messages if "SBOM" in m), {})
stdlib_version_known = sbom.get("go_version", "").startswith("go1.")
called = [f for f in findings if any(frame.get("function") for frame in f.get("trace", []))]
status = {"status":"INCONCLUSIVE" if not complete else "CALLED_FINDINGS" if called else "PARTIAL" if not stdlib_version_known else "MODULE_FINDINGS" if findings else "COMPLETE",
          "exit_code":code,"called_findings":len(called),"module_findings":len(findings),"stdlib_version_known":stdlib_version_known,"sbom_go_version":sbom.get("go_version"),"findings":findings}
(evidence/"status.json").write_text(json.dumps(status,indent=2)+"\n")
print(json.dumps(status))
raise SystemExit(1 if called else 0 if complete and stdlib_version_known else 2)
