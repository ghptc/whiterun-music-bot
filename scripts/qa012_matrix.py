#!/usr/bin/env python3
"""Opt-in offline RSS controls. Supply NEW root and prebuilt qa-memory binaries.
Run inside an independently resource-limited QA scope; never reads .env.
"""
import argparse
import datetime
import json
import pathlib
import subprocess
import time

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--out', required=True, type=pathlib.Path)
p.add_argument('--context', action='store_true', help='context and goroutine attribution only')
p.add_argument('--isolate', action='store_true', help='fixed-count SQLite and defer controls only')
p.add_argument('--native', required=True, type=pathlib.Path)
p.add_argument('--race', required=True, type=pathlib.Path)
a = p.parse_args()
a.out.mkdir(mode=0o700)
env = {'PATH': '/usr/bin:/bin', 'GOMAXPROCS': '2', 'GOMEMLIMIT': '256MiB',
       'GORACE': 'atexit_sleep_ms=0'}
cases = [
    ('baseline-native', a.native, ['-mode', 'baseline', '-duration', '60s']),
    ('baseline-race', a.race, ['-mode', 'baseline', '-duration', '60s']),
    ('warm-native', a.native, ['-duration', '180s']),
    ('warm-race', a.race, ['-duration', '180s']),
    ('cold-native', a.native, ['-duration', '180s', '-warm=false']),
    ('disabled-native', a.native, ['-duration', '180s', '-warm=false', '-no-cache']),
    ('disabled-race', a.race, ['-duration', '180s', '-warm=false', '-no-cache']),
    ('simulated-native', a.native, ['-mode', 'simulated', '-count', '100000']),
    ('simulated-race', a.race, ['-mode', 'simulated', '-count', '100000']),
    ('cycles-native', a.native, ['-mode', 'cycles', '-count', '10000']),
    ('cycles-race', a.race, ['-mode', 'cycles', '-count', '10000']),
]
if a.isolate:
    cases = [
        ('sqlite-native', a.native, ['-mode', 'sqlite', '-count', '300']),
        ('sqlite-race', a.race, ['-mode', 'sqlite', '-count', '300']),
        ('plain-native', a.native, ['-mode', 'plain', '-count', '5000000']),
        ('plain-race', a.race, ['-mode', 'plain', '-count', '5000000']),
        ('defers-native', a.native, ['-mode', 'defers', '-count', '5000000']),
        ('defers-race', a.race, ['-mode', 'defers', '-count', '5000000']),
    ]
if a.context:
    cases = [
        ('sqlite-background-native', a.native, ['-mode', 'sqlite', '-count', '300', '-sqlite-background']),
        ('sqlite-background-race', a.race, ['-mode', 'sqlite', '-count', '300', '-sqlite-background']),
        ('goroutines-native', a.native, ['-mode', 'goroutines', '-count', '150000']),
        ('goroutines-race', a.race, ['-mode', 'goroutines', '-count', '150000']),
    ]
for name, binary, args in cases:
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    before = time.monotonic()
    with (a.out / (name + '.log')).open('w') as log:
        command = [str(binary.resolve()), '-out', str((a.out / name).resolve()),
                   '-idle', '30s'] + args
        r = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT,
                           timeout=600, check=False)
    row = {'name': name, 'command': command, 'started_at': started,
           'real_seconds': time.monotonic() - before, 'exit_code': r.returncode}
    with (a.out / 'execution.jsonl').open('a') as f:
        f.write(json.dumps(row) + '\n')
    if r.returncode:
        raise SystemExit(r.returncode)
