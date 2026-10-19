#!/usr/bin/env python3
"""Record govulncheck output without treating missing tools/network as success."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess

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
version = subprocess.run([tool,"-version"],capture_output=True,text=True,timeout=30)
(evidence/"version.txt").write_text(version.stdout+version.stderr)
try:
    run = subprocess.run([tool,"-json","./..."],cwd=ROOT,capture_output=True,text=True,timeout=300)
    output, code = run.stdout+run.stderr, run.returncode
except subprocess.TimeoutExpired as exc:
    output, code = str(exc), 124
(evidence/"govulncheck.jsonl").write_text(output)
# JSON findings must be investigated: exit code zero alone in JSON mode is not
# a finding count, and an incomplete/no-progress stream is not a clean scan.
messages = []
for line in output.splitlines():
    try:
        messages.append(json.loads(line))
    except json.JSONDecodeError:
        pass
# govulncheck emits pretty JSON objects, so also decode concatenated objects.
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
status = {"status":"FINDINGS" if complete and findings else "COMPLETE" if complete else "INCONCLUSIVE",
          "exit_code":code,"findings":findings}
(evidence/"status.json").write_text(json.dumps(status,indent=2)+"\n")
print(json.dumps(status))
raise SystemExit(1 if findings else 0 if complete else 2)
