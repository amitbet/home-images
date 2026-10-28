"""Compare a native SQLite HNSW extension with actual Go rankSelection results."""
import importlib.metadata
import json
import sqlite3
import statistics
import time
from pathlib import Path

import numpy as np
import psutil
import vectorlite_py

HERE = Path(__file__).resolve().parent
DATA = HERE / 'data'
load = lambda name: json.loads((DATA / name).read_text())
manifest, queries, cases = load('manifest.json'), load('queries.json'), load('cases.json')
ground = {(x['Case'], x['Query']): x for x in load('go-results.json')}
conn = sqlite3.connect(':memory:', uri=True)
conn.enable_load_extension(True)
vectorlite_py.load_vectorlite(conn)
info = conn.execute('select vectorlite_info()').fetchone()[0]
conn.execute('ATTACH DATABASE ? AS raw', ((DATA / 'vectors.sqlite3').as_uri() + '?mode=ro',))
conn.execute('ATTACH DATABASE ? AS ai', ((DATA / 'ai.sqlite3').as_uri() + '?mode=ro',))
conn.execute('ATTACH DATABASE ? AS catalog', ((DATA / 'index-go.sqlite3').as_uri() + '?mode=ro',))
conn.execute('CREATE TABLE candidates(name TEXT,rowid INTEGER,PRIMARY KEY(name,rowid)) WITHOUT ROWID')
for case in cases:
    if not case['name'].startswith('all_'):
        conn.executemany('INSERT INTO candidates VALUES(?,?)', [(case['name'], rowid) for rowid in case['rowids']])
conn.commit()
rows = conn.execute('SELECT rowid,embedding FROM raw.vectors ORDER BY rowid').fetchall()
process = psutil.Process()
rss_before = process.memory_info().rss
index_path = DATA / 'hnsw.bin'
index_path.unlink(missing_ok=True)
# PyPI 0.2.0 uses the legacy third-argument persistence API.
ddl = f"CREATE VIRTUAL TABLE v USING vectorlite(embedding float32[{manifest['dimensions']}] cosine,hnsw(max_elements={manifest['vectors']+1000},M=16,ef_construction=200,random_seed=100),'{index_path.as_posix()}')"
conn.execute(ddl)
start = time.perf_counter()
for j, row in enumerate(rows):
    conn.execute('INSERT INTO v(rowid,embedding) VALUES(?,?)', row)
    if (j+1) % 5000 == 0:
        print('built', j+1, 'elapsed_s', round(time.perf_counter()-start,1), flush=True)
conn.commit()
build_s = time.perf_counter() - start
rss_after = process.memory_info().rss
del rows
print('build_s', round(build_s,2), 'rss_delta_MiB', round((rss_after-rss_before)/2**20,1), flush=True)

def summary(times):
    return dict(median_ms=statistics.median(times), p95_ms=float(np.percentile(times,95)), min_ms=min(times), max_ms=max(times), samples=len(times))

result = dict(manifest=manifest, extension=info, sqlite=sqlite3.sqlite_version,
              packages={x:importlib.metadata.version(x) for x in ['vectorlite-py','numpy','psutil']},
              build_s=build_s, index_rss_delta_mib=(rss_after-rss_before)/2**20, cases=[])

for case in cases:
    filtered = not case['name'].startswith('all_')
    filter_sql = ' AND v.rowid IN (SELECT rowid FROM candidates WHERE name=?)' if filtered else ''
    sql = 'SELECT v.rowid,v.distance,r.image_id FROM v CROSS JOIN raw.vectors r ON r.rowid=v.rowid WHERE knn_search(v.embedding,knn_param(?,?,?))' + filter_sql + ' ORDER BY v.distance'
    # Cost of deriving current filter membership from metadata, separately from graph traversal.
    filter_times = []
    if filtered:
        eligibility_sql = "SELECT r.rowid FROM catalog.images i JOIN ai.ai_images a ON a.image_id=i.id AND a.signature=i.signature JOIN raw.vectors r ON r.image_id=i.id WHERE a.models=? AND a.error='' AND " + case['clause'].replace('FROM faces ', 'FROM ai.faces ').replace('JOIN ai_images ', 'JOIN ai.ai_images ')
        for _ in range(5):
            start=time.perf_counter()
            candidate_ids={x[0] for x in conn.execute(eligibility_sql, [manifest['model']]+case['args'])}
            filter_times.append((time.perf_counter()-start)*1000)
            assert candidate_ids == set(case['rowids'])
    native_times, native_recalls = [], []
    native_sql = "SELECT i.id,vector_distance(a.clip,?,'cosine') AS distance FROM catalog.images i JOIN ai.ai_images a ON a.image_id=i.id AND a.signature=i.signature WHERE a.models=? AND a.error='' AND " + case['clause'].replace('FROM faces ', 'FROM ai.faces ').replace('JOIN ai_images ', 'JOIN ai.ai_images ') + ' ORDER BY distance,i.id LIMIT ?'
    for j, query in enumerate(queries):
        vector = np.asarray(query['vector'],dtype='<f4')
        args = [vector.tobytes(),manifest['model']]+case['args']+[case['k']]
        if j==0:
            conn.execute(native_sql,args).fetchall()
        start=time.perf_counter(); exact=conn.execute(native_sql,args).fetchall(); native_times.append((time.perf_counter()-start)*1000)
        expected={x['ID'] for x in ground[(case['name'],j)]['Hits']}
        native_recalls.append(len(expected & {x[0] for x in exact})/len(expected))
    entry = dict(name=case['name'], eligible=case['eligible'], k=case['k'], go=summary([ground[(case['name'],j)]['MS'] for j in range(len(queries))]), native_exact=dict(**summary(native_times),mean_recall=statistics.mean(native_recalls)), filter_resolution=summary(filter_times) if filter_times else None, hnsw=[])
    print(case['name'],'native_exact_median_ms',round(entry['native_exact']['median_ms'],3),flush=True)
    for ef in [100, 500, 1000, 2000]:
        latencies, recalls, per_query = [], [], []
        for j, query in enumerate(queries):
            v = np.asarray(query['vector'],dtype='<f4')
            v /= np.float32(np.linalg.norm(v.astype(np.float64)))
            args = [v.tobytes(), min(case['k'],case['eligible']), ef] + ([case['name']] if filtered else [])
            expected = {x['ID'] for x in ground[(case['name'],j)]['Hits']}
            conn.execute(sql,args).fetchall() # warmup
            times=[]
            for _ in range(5):
                start=time.perf_counter(); hits=conn.execute(sql,args).fetchall(); times.append((time.perf_counter()-start)*1000)
            recall=len(expected & {x[2] for x in hits})/len(expected)
            recalls.append(recall);latencies.extend(times)
            per_query.append(dict(query=j,recall=recall,times_ms=times))
        metrics=dict(ef=ef, effective_ef=max(ef,case['k']), **summary(latencies), mean_recall=statistics.mean(recalls), min_recall=min(recalls), queries=per_query)
        entry['hnsw'].append(metrics)
        print(case['name'],'ef',ef,'median_ms',round(metrics['median_ms'],3),'p95_ms',round(metrics['p95_ms'],3),'recall',round(metrics['mean_recall'],4),flush=True)
    result['cases'].append(entry)
    (DATA/'results.json').write_text(json.dumps(result,indent=2))
start=time.perf_counter()
conn.close() # legacy persistence serializes the graph here
result['save_s']=time.perf_counter()-start
result['index_mib']=index_path.stat().st_size/2**20
conn=sqlite3.connect(':memory:');conn.enable_load_extension(True);vectorlite_py.load_vectorlite(conn)
start=time.perf_counter();conn.execute(ddl)
result['warm_os_cache_load_s']=time.perf_counter()-start
conn.close()
(DATA/'results.json').write_text(json.dumps(result,indent=2))
# Public output contains only aggregate timings and recall, never personal IDs/vectors.
public=json.loads(json.dumps(result))
for case in public['cases']:
    for row in case['hnsw']:
        del row['queries']
(HERE/'results.json').write_text(json.dumps(public,indent=2))
print('saved results.json',flush=True)
