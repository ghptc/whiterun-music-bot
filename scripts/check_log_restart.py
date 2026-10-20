#!/usr/bin/env python3
"""Isolated real-container restart acceptance. Never mounts production data.

Requires a built runtime image and Docker. No Discord/API credentials or network.
Creates and removes only its own uniquely named container and temporary volume.
"""
import json
import os
import subprocess
import uuid

image = os.environ.get("BARD_TEST_IMAGE", "whiterun-music-bot-bard:latest")
name = "bard-log-cr008-" + uuid.uuid4().hex[:12]
volume = name + "-data"
path = "/home/bard/data/bard-logs.db"


def docker(*args, check=True):
    result = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=30)
    if check and result.returncode:
        raise RuntimeError(f"Docker {args[0]} failed: {result.stderr.strip()}")
    return result


def read():
    output = docker("run", "--rm", "--network", "none", "--read-only",
                    "-v", volume + ":/home/bard/data", image,
                    "logs", "--db", path, "--last", "100").stdout
    return [json.loads(line) for line in output.splitlines() if line]


created_volume = False
created_container = False
try:
    docker("volume", "create", volume)
    created_volume = True
    docker("create", "--name", name, "--network", "none", "--read-only",
           "--tmpfs", "/tmp", "-v", volume + ":/home/bard/data",
           "-e", "BARD_DB_PATH=/home/bard/data/bard.db",
           "-e", "BARD_LOG_DB_PATH=" + path,
           "-e", "BARD_LOG_DB_MAX_BYTES=1048576", image)
    created_container = True
    # An unconfigured bot deliberately exits 1 after logging and flushing its
    # startup error. This exercises the real main(), without connecting to Discord.
    for cycle in (1, 2):
        docker("start", "-a", name, check=False)
        code = docker("inspect", "--format", "{{.State.ExitCode}}", name).stdout.strip()
        if code != "1":
            raise AssertionError(f"Unexpected startup exit: {code}")
        rows = read()
        if len(rows) != cycle * 2:
            raise AssertionError(f"Missing persisted startup events: {len(rows)}")
        if rows[-1]["event"] != "bot stopped" or rows[-1]["id"] != cycle * 2:
            raise AssertionError("Restart lost events or reused sequence IDs")
        print(json.dumps({"cycle": cycle, "rows": len(rows), "latest_id": rows[-1]["id"]}))
    result = docker("run", "--rm", "--network", "none", "--read-only", "-v",
                    volume + ":/home/bard/data", "--entrypoint", "python3", image, "-c",
                    'import sqlite3,os,json; p="/home/bard/data/bard-logs.db"; '
                    'db=sqlite3.connect("file:"+p+"?mode=ro",uri=True); '
                    'print(json.dumps({"integrity":db.execute("PRAGMA integrity_check").fetchone()[0],'
                    '"mode":oct(os.stat(p).st_mode & 0o777),'
                    '"operational_db_created":os.path.exists("/home/bard/data/bard.db")}))')
    evidence = json.loads(result.stdout)
    if evidence != {"integrity": "ok", "mode": "0o600", "operational_db_created": False}:
        raise AssertionError(evidence)
    print(json.dumps(evidence))
    print("PASS real container restart, historical read-only query, permissions and SQLite integrity")
finally:
    if created_container:
        docker("rm", "-f", name, check=False)
    if created_volume:
        docker("volume", "rm", volume, check=False)
