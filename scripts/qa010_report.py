#!/usr/bin/env python3
"""Export synthetic evidence (never SQLite/WAV/credentials), reconcile events."""
import argparse
import collections
import json
from pathlib import Path
import shutil

parser = argparse.ArgumentParser()
parser.add_argument('--run', required=True)
parser.add_argument('--out', required=True)
args = parser.parse_args()
source, dest = Path(args.run), Path(args.out)
marker = json.loads((source / 'qa010-owned.json').read_text())
dest.mkdir(parents=True, exist_ok=True)
(dest/'config.json').write_text(json.dumps(marker, indent=2)+'\n')
records = []
for segment in sorted(source.iterdir()):
    if not segment.is_dir():
        continue
    output = dest/segment.name
    output.mkdir(exist_ok=True)
    for filename in ('summary.json', 'events.jsonl', 'resources.jsonl'):
        if (segment/filename).exists():
            shutil.copy2(segment/filename, output/filename)
    if not (segment/'summary.json').exists():
        records.append(dict(segment=segment.name, status='incomplete_no_final_summary'))
        continue
    summary = json.loads((segment/'summary.json').read_text())
    events = [json.loads(line) for line in (segment/'events.jsonl').read_text().splitlines()]
    counts = collections.Counter()
    selected, announced, terminal = set(), set(), set()
    for event in events:
        kind = event['event']
        if kind == 'batch_accepted':
            continue
        key = (event['guild'], event['identity'])
        counts[(event['guild'], kind)] += 1
        if kind == 'selected':
            assert key not in selected, ('unexpected duplicate selection', key)
            selected.add(key)
        elif kind == 'announced':
            assert key in selected and key not in announced, ('orphan/duplicate announcement', key)
            announced.add(key)
        elif kind == 'terminal':
            assert key in selected and key not in terminal, ('orphan/duplicate terminal', key)
            assert event['frames'] > 0 or key not in announced
            terminal.add(key)
    assert selected == terminal, 'selected attempt without terminal result'
    for guild in summary['guilds']:
        gid = guild['guild']
        assert guild['selected'] > 0 and guild['announced'] > 0, 'vacuous playback evidence'
        for field, event in [('selected', 'selected'), ('announced', 'announced')]:
            assert guild[field] == counts[(gid, event)]
        assert guild['selected'] == sum(guild['terminal'].values())
        assert guild['accepted'] == guild['selected'] + guild['pending']
    samples = [json.loads(line) for line in (segment/'resources.jsonl').read_text().splitlines()]
    metrics = {}
    for name in ['rss_bytes','heap_bytes','goroutines','file_descriptors','child_processes','threads','cpu_seconds','reaped_children_cpu_seconds']:
        values = [row[name] for row in samples if name in row]
        if values:
            metrics[name] = dict(baseline=values[0], peak=max(values), after_cleanup=values[-1])
    record = dict(segment=segment.name, status=summary['status'],
                  continuous_seconds=summary['real_elapsed_seconds'],
                  target_completed=summary['continuous_target_completed'],
                  resource_samples=len(samples), resources=metrics,
                  selected=sum(g['selected'] for g in summary['guilds']),
                  announced=sum(g['announced'] for g in summary['guilds']),
                  completed=sum(g['terminal'].get('completed',0) for g in summary['guilds']),
                  pending=sum(g['pending'] for g in summary['guilds']),
                  ledger_reconciled=True)
    records.append(record)
    (output/'analysis.json').write_text(json.dumps(record,indent=2)+'\n')
(dest/'segments.json').write_text(json.dumps(records,indent=2)+'\n')
print(json.dumps(records,indent=2))
raise SystemExit(any(r['status']!='pass' or not r.get('target_completed') for r in records))
