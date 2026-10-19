#!/usr/bin/env python3
"""Cover the actual bot binary's offline startup validation paths, never login."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "docs/qa/advanced/evidence/binary"
EVIDENCE.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix="bard-qa008-binary-") as temporary:
    work = Path(temporary)
    binary = work / "bard"
    build = ["go", "build", "-cover", "-coverpkg=./...", "-o", str(binary), "./cmd/bot"]
    result = subprocess.run(build, cwd=ROOT, text=True, capture_output=True, timeout=120)
    (EVIDENCE / "build.txt").write_text(result.stdout + result.stderr)
    if result.returncode:
        raise SystemExit(result.returncode)
    cov = work / "coverage"
    cov.mkdir()
    tools = work / "tools"
    tools.mkdir()
    for name in ("yt-dlp", "ffmpeg", "deno"):
        path = tools / name
        # An accidental execution must fail, rather than access any provider.
        path.write_text("#!/bin/sh\nexit 99\n")
        path.chmod(0o700)
    corrupt = work / "corrupt.db"
    corrupt.write_bytes(b"not a SQLite database")
    fixtures = [
        ("missing-token", [], {}, "DISCORD_TOKEN is required"),
        ("invalid-application", [], {"DISCORD_TOKEN":"MQ.fixture.fixture"}, "DISCORD_APPLICATION_ID"),
        ("invalid-guild", [], {"DISCORD_TOKEN":"MQ.fixture.fixture","DISCORD_APPLICATION_ID":"1","DISCORD_GUILD_ID":"bad"}, "DISCORD_GUILD_ID"),
        ("missing-runtime", [], {"DISCORD_TOKEN":"MQ.fixture.fixture","DISCORD_APPLICATION_ID":"1","PATH":str(work/"empty")}, "required runtime yt-dlp"),
        ("invalid-policy-json", [], {"DISCORD_TOKEN":"MQ.fixture.fixture","DISCORD_APPLICATION_ID":"1","SPOTIFY_MATCH_POLICY":"{"}, "SPOTIFY_MATCH_POLICY"),
        ("invalid-policy-values", [], {"DISCORD_TOKEN":"MQ.fixture.fixture","DISCORD_APPLICATION_ID":"1","SPOTIFY_MATCH_POLICY":'{"MinScore":1}'}, "invalid Spotify match policy"),
        ("corrupt-sqlite", [], {"DISCORD_TOKEN":"MQ.fixture.fixture","DISCORD_APPLICATION_ID":"1","BARD_DB_PATH":str(corrupt)}, "database"),
        ("diagnose-no-arguments", ["spotify-diagnose"], {}, "requires playlist URLs"),
        ("diagnose-rejected-url", ["spotify-diagnose","https://open.spotify.com/not-a-resource"], {}, "Invalid Spotify link"),
    ]
    records = []
    for name, args, override, expected in fixtures:
        # Explicit whitelist: no real credentials, home config or production DB.
        env = {"PATH":str(tools),"GOCOVERDIR":str(cov),"BARD_DB_PATH":str(work/"never-open-production.db")}
        env.update(override)
        run = subprocess.run([str(binary),*args], env=env, cwd=work,
                             text=True, capture_output=True, timeout=10)
        output = run.stdout + run.stderr
        (EVIDENCE / (name+".txt")).write_text(output)
        if run.returncode != 1 or expected not in output:
            raise SystemExit(f"Unexpected offline startup outcome: {name}: {run.returncode}\n{output}")
        records.append({"case":name,"exit_code":run.returncode,"expected":expected})
    (EVIDENCE / "scenarios.json").write_text(json.dumps(records,indent=2)+"\n")
    for mode, filename in (("percent","percent.txt"),("func","functions.txt"),("textfmt","integration.cover")):
        command = ["go","tool","covdata",mode,"-i="+str(cov)]
        if mode == "textfmt":
            command += ["-o="+str(EVIDENCE/filename)]
        result = subprocess.run(command,cwd=ROOT,capture_output=True,text=True,timeout=30)
        if mode != "textfmt" or result.returncode:
            (EVIDENCE/filename).write_text(result.stdout+result.stderr)
        if result.returncode:
            raise SystemExit(result.returncode)
    print(f"PASS {len(records)} offline binary scenarios; coverage saved separately")
