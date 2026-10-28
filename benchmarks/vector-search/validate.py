"""Verify the serialized graph can serve the same real query after reopening."""
import json
import sqlite3
from pathlib import Path
import numpy as np
import vectorlite_py

HERE=Path(__file__).resolve().parent
DATA=HERE/'data'
result=json.loads((HERE/'results.json').read_text())
assert len(result['cases'])==4
assert all(c['go']['samples']==20 and len(c['hnsw'])==4 for c in result['cases'])
conn=sqlite3.connect(':memory:',uri=True)
conn.enable_load_extension(True)
vectorlite_py.load_vectorlite(conn)
conn.execute('ATTACH DATABASE ? AS raw',((DATA/'vectors.sqlite3').as_uri()+'?mode=ro',))
m=result['manifest']
conn.execute(f"CREATE VIRTUAL TABLE v USING vectorlite(embedding float32[{m['dimensions']}] cosine,hnsw(max_elements={m['vectors']+1000},M=16,ef_construction=200,random_seed=100),'{(DATA/'hnsw.bin').as_posix()}')")
q=json.loads((DATA/'queries.json').read_text())[0]
v=np.asarray(q['vector'],dtype='<f4');v/=np.float32(np.linalg.norm(v.astype(np.float64)))
hits=conn.execute('SELECT r.image_id FROM v CROSS JOIN raw.vectors r ON r.rowid=v.rowid WHERE knn_search(v.embedding,knn_param(?,60,1000))',(v.tobytes(),)).fetchall()
assert len(hits)==len(set(x[0] for x in hits))==60
exact=next(x for x in json.loads((DATA/'go-results.json').read_text()) if x['Case']=='all_top60' and x['Query']==0)
recall=len({x[0] for x in hits}&{x['ID'] for x in exact['Hits']})/60
assert recall>=0.95,recall
conn.close()
print('Reopened persisted graph: 60 distinct results; recall@60 =',recall)
