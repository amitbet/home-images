"""Verify exact before/after Go outputs and publish aggregate timings."""
import json
import statistics
from pathlib import Path
HERE=Path(__file__).resolve().parent
DATA=HERE/'data'
before=json.loads((DATA/'go-results.json').read_text())
after=json.loads((DATA/'go-results-optimized.json').read_text())
assert len(before)==len(after)==80
assert all((a['Case'],a['Query'],a['Total'],a['Hits'])==(b['Case'],b['Query'],b['Total'],b['Hits']) for a,b in zip(before,after))
rows=[]
for name in dict.fromkeys(x['Case'] for x in before):
    a=statistics.median(x['MS'] for x in before if x['Case']==name)
    b=statistics.median(x['MS'] for x in after if x['Case']==name)
    rows.append(dict(case=name,before_median_ms=a,after_median_ms=b,speedup=a/b))
(HERE/'optimization-results.json').write_text(json.dumps(dict(queries=80,exact_outputs_identical=True,results=rows),indent=2))
lines=['# Go search optimizations — 2026-10-27','','All 80 query results have identical candidate counts, ordered photo IDs and scores.','','| Search | Before median | After median | Speedup |','|---|---:|---:|---:|']
for row in rows:
    lines.append(f"| {row['case']} | {row['before_median_ms']:.2f} ms | {row['after_median_ms']:.2f} ms | {row['speedup']:.1f}× |")
lines+=['','Same 58,778-vector snapshot and 20 text queries per case as the original experiment. Times include actual Go SQL joins, reading vectors, decoding, dot products, and ranking. Text inference and API response formatting are excluded. Normal background service work continued.','','Changes: exact-directory covering index; person-first joins with covering indexes; planner statistics maintenance. No approximate vector index is used.']
(HERE/'OPTIMIZATIONS.md').write_text('\n'.join(lines)+'\n',encoding='utf-8')
print('\n'.join(lines))
