#!/usr/bin/env python3
"""Optional standalone resource plot; requires matplotlib, not used by CI tests."""
import json
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
ROOT=Path(__file__).resolve().parents[1]/'docs/qa/endurance/evidence'
fig,axes=plt.subplots(2,2,figsize=(11,7),layout='constrained')
for name in ['soak-123','soak-500']:
    segment=sorted((ROOT/name).glob('*/resources.jsonl'))[0]
    rows=[json.loads(line) for line in segment.read_text().splitlines()]
    for ax,field,label,scale in zip(axes.flat,['rss_bytes','heap_bytes','goroutines','file_descriptors'],['RSS (MiB)','Go heap (MiB)','Goroutines','File descriptors'],[2**20,2**20,1,1]):
        ax.plot([r['elapsed_seconds'] for r in rows],[r[field]/scale for r in rows],marker='.',label=name)
        ax.set(xlabel='Real seconds',ylabel=label);ax.grid(alpha=.2)
axes[0,0].legend()
fig.suptitle('QA-010: short local runs with race detector; final point after cleanup/GC')
fig.savefig(ROOT/'resource-trends.png',dpi=160)
