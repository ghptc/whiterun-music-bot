#!/usr/bin/env python3
"""Summarize qa-memory artifacts; retain raw values and never infer a leak from RSS."""
import argparse
import json
import pathlib
import re


def mappings(path):
    rows = []
    for line in path.read_text().splitlines():
        if re.match(r'^[0-9a-f]+-[0-9a-f]+ ', line):
            fields = line.split(maxsplit=5)
            rows.append({'mapping': line, 'name': fields[5] if len(fields) == 6 else 'anonymous'})
        elif ':' in line and rows:
            key, value = line.split(':', 1)
            parts = value.split()
            if parts and parts[0].isdigit():
                rows[-1][key] = int(parts[0]) * (1024 if len(parts) > 1 and parts[1] == 'kB' else 1)
    grouped = {}
    for row in rows:
        name = row['name']
        group = grouped.setdefault(name, {'rss_bytes': 0, 'size_bytes': 0, 'anonymous_bytes': 0})
        for key, field in [('rss_bytes', 'Rss'), ('size_bytes', 'Size'), ('anonymous_bytes', 'Anonymous')]:
            group[key] += row.get(field, 0)
    return {'groups': grouped, 'largest_resident': sorted(rows, key=lambda r: r.get('Rss', 0), reverse=True)[:12]}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--run', required=True, type=pathlib.Path)
    p.add_argument('--out', required=True, type=pathlib.Path)
    a = p.parse_args()
    result = {}
    for manifest in sorted(a.run.rglob('diagnostic.json')):
        root = manifest.parent
        samples = [json.loads(line) for line in (root / 'memory.jsonl').read_text().splitlines()]
        rows = []
        for sample in samples:
            m = sample['memstats']
            rollup = {line.split(':')[0]: int(line.split()[1]) * 1024
                      for line in sample['smaps_rollup'].splitlines()[1:] if len(line.split()) == 3}
            rows.append({'phase': sample['phase'], 'elapsed_seconds': sample['elapsed_seconds'],
                         'rss_bytes': rollup.get('Rss'), 'anonymous_bytes': rollup.get('Anonymous'),
                         'heap_alloc': m['HeapAlloc'], 'heap_inuse': m['HeapInuse'],
                         'heap_sys': m['HeapSys'], 'heap_idle': m['HeapIdle'],
                         'heap_released': m['HeapReleased'], 'sys': m['Sys'],
                         'total_alloc': m['TotalAlloc'], 'num_gc': m['NumGC'],
                         'gc_cpu_fraction': m['GCCPUFraction'],
                         'goroutines_created': sample['runtime_metrics'].get('/sched/goroutines-created:goroutines'), 'goroutines': sample['goroutines']})
        periodic = [r for r in rows if r['phase'] == 'periodic']
        slopes = []
        for lo, hi in [(0, 600), (600, 1800), (1800, 3000), (3000, 3601)]:
            window = [r for r in periodic if lo <= r['elapsed_seconds'] < hi]
            if len(window) >= 2:
                x = [r['elapsed_seconds'] for r in window]
                y = [r['rss_bytes'] for r in window]
                xm, ym = sum(x) / len(x), sum(y) / len(y)
                slope = sum((xx - xm) * (yy - ym) for xx, yy in zip(x, y)) / sum((xx - xm)**2 for xx in x)
                slopes.append({'window_seconds': [lo, hi], 'samples': len(x), 'rss_mib_per_minute': slope * 60 / 2**20})
        result[str(root.relative_to(a.run))] = {'manifest': json.loads(manifest.read_text()),
            'samples': rows, 'rss_slopes': slopes,
            'smaps': {f.stem: mappings(f) for f in root.glob('*-smaps.txt')}}
    a.out.write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
