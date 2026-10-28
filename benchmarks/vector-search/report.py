"""Generate a shareable aggregate-only report and interactive comparison."""
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
data = json.loads((HERE / 'results.json').read_text())
lines = ['# Vector search benchmark — 2026-10-27', '',
         'Intel N150 · Windows amd64 · 58,778 real CLIP vectors · 512 dimensions · 20 text queries.', '',
         'All numbers are retrieval-only warm-search measurements. Production code and deployment are unchanged.', '',
         '| Case | Candidates | Go exact median / p95 ms | Native SQLite exact median / p95 ms |',
         '|---|---:|---:|---:|']
for case in data['cases']:
    a,b=case['go'],case['native_exact']
    lines.append(f"| {case['name']} | {case['eligible']:,} | {a['median_ms']:.2f} / {a['p95_ms']:.2f} | {b['median_ms']:.2f} / {b['p95_ms']:.2f} |")
lines += ['', '| Case | ef | HNSW median / p95 ms | Mean recall | Worst query recall | Speedup vs Go median |', '|---|---:|---:|---:|---:|---:|']
for case in data['cases']:
    for row in case['hnsw']:
        lines.append(f"| {case['name']} | {row['ef']} | {row['median_ms']:.3f} / {row['p95_ms']:.3f} | {row['mean_recall']:.2%} | {row['min_recall']:.2%} | {case['go']['median_ms']/row['median_ms']:.0f}× |")
lines += ['', 'Filtered HNSW timings above use cached candidate rowids. Resolving those rowids from current metadata adds:', '']
for case in data['cases']:
    f=case['filter_resolution']
    if f:
        lines.append(f"- {case['name']}: median {f['median_ms']:.2f} ms, p95 {f['p95_ms']:.2f} ms (5 samples).")
lines += ['', f"Build: {data['build_s']:.2f} s. Index: {data['index_mib']:.2f} MiB. Approximate process working-set increase while building: {data['index_rss_delta_mib']:.2f} MiB. Save: {data['save_s']:.2f} s. Warm-OS-cache reload: {data['warm_os_cache_load_s']:.2f} s.", '',
          f"Extension: {data['extension']}. Native SQLite {data['sqlite']}. Go uses modernc.org/sqlite v1.61.0.", '',
          'The native exact control retains an exhaustive scan and the original catalog/model/signature joins. HNSW uses the prevalidated snapshot index and a separate ID lookup. This comparison therefore includes both index benefit and avoided per-query eligibility work.', '',
          'Recall measures agreement with exact embedding neighbors, not semantic relevance. No text inference, browser/API response, thumbnails, concurrency test, or controlled cold-disk test is included. Normal background service activity continued. The native extension is not integrated with the Go driver. See README.md for methodology and reproduction.']
(HERE/'RESULTS.md').write_text('\n'.join(lines)+'\n',encoding='utf-8')
html='''<!doctype html><html><head><meta charset="utf-8"><style>
*{box-sizing:border-box}body{margin:0;font:14px var(--font-sans,system-ui);color:var(--foreground,#222)}p{line-height:1.5;margin:0 0 14px}.muted{color:var(--muted-foreground,#666);font-size:12px}label{display:inline-flex;gap:8px;align-items:center;margin:0 16px 14px 0}select{font:inherit;padding:6px;background:var(--background,#fff);color:inherit;border:1px solid var(--border,#ccc);border-radius:6px}table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums;margin:0 0 14px}th,td{text-align:right;padding:10px 7px;border-bottom:1px solid var(--border,#ddd)}th:first-child,td:first-child{text-align:left;padding-left:0}th{font-size:12px;color:var(--muted-foreground,#666);font-weight:500}.big{font-size:22px;font-weight:650;color:var(--accent-foreground,var(--foreground))}.detail{font-size:12px;margin-bottom:12px}#note{line-height:1.5} @media(max-width:480px){th,td{padding:9px 3px;font-size:12px}label{font-size:12px}select{max-width:210px}.big{font-size:20px}}
</style></head><body>
<p><strong>Measured on your photo library</strong><br><span class="muted">58,778 embeddings · 512 dimensions · Intel N150 · 20 text queries</span></p>
<label>Search <select id="which"><option value="0">Whole library · top 60</option><option value="1">Whole library · top 500</option><option value="2">Directory · top 60</option><option value="3">Person · top 60</option></select></label>
<label>HNSW ef <select id="ef"><option>100</option><option>500</option><option selected>1000</option><option>2000</option></select></label>
<table><thead><tr><th>Retrieval method</th><th>Median</th><th>p95</th><th>Exact matches retained</th></tr></thead><tbody id="rows"></tbody></table>
<p id="gain"></p><p id="note" class="detail"></p><p id="build" class="muted"></p>
<p class="muted">Warm retrieval only; text embedding, API response and thumbnails excluded. HNSW recall is agreement with exact top-k results, averaged across queries. The service stayed running; no deployment changes.</p>
<script>const data=__DATA__;const $=id=>document.getElementById(id);const ms=n=>n.toFixed(n<10?2:0)+' ms';function update(){const c=data.cases[+$('which').value],h=c.hnsw.find(x=>x.ef===+$('ef').value);const row=(name,m,rec)=>`<tr><td>${name}</td><td>${ms(m.median_ms)}</td><td>${ms(m.p95_ms)}</td><td>${rec}</td></tr>`;$('rows').innerHTML=row('Current Go scan',c.go,'100%')+row('Native SQLite exact scan',c.native_exact,(c.native_exact.mean_recall*100).toFixed(1)+'%')+row('HNSW index',h,(h.mean_recall*100).toFixed(1)+'%');$('gain').innerHTML=`<span class="big">${Math.round(c.go.median_ms/h.median_ms).toLocaleString()}×</span> faster retrieval with HNSW${c.filter_resolution?' when candidate filters are cached':''}.`;$('note').textContent=c.filter_resolution?`Filter caveat: the index uses a cached set of ${c.eligible.toLocaleString()} eligible photo IDs. Resolving that set from metadata takes another ${ms(c.filter_resolution.median_ms)} median. These lookup timings exclude that cost.`:`HNSW skips most vector comparisons. At this setting the worst query retained ${(h.min_recall*100).toFixed(1)}% of exact matches. The index contains the fixed, prevalidated snapshot; production must keep eligibility synchronized.`;}$('which').onchange=update;$('ef').onchange=update;$('build').textContent=`One-time build: ${data.build_s.toFixed(1)} s · Saved index: ${data.index_mib.toFixed(0)} MiB · Warm reload: ${data.warm_os_cache_load_s.toFixed(2)} s. Tested Vectorlite 0.2.0 separately; Go integration is still required.`;update();</script></body></html>'''.replace('__DATA__',json.dumps(data))
(HERE/'report.html').write_text(html,encoding='utf-8')
print('\n'.join(lines))
