"""Explain and time metadata-only alternatives on benchmark snapshots only."""
import json
import sqlite3
import statistics
import time
from pathlib import Path

HERE=Path(__file__).resolve().parent
DATA=HERE/'data'
manifest=json.loads((DATA/'manifest.json').read_text())
cases=json.loads((DATA/'cases.json').read_text())
model=manifest['model']
db=sqlite3.connect(DATA/'ai.sqlite3')
db.execute('ATTACH DATABASE ? AS catalog',(str(DATA/'index-go.sqlite3'),))
base="SELECT i.id FROM catalog.images i JOIN ai_images a ON a.image_id=i.id AND a.signature=i.signature WHERE a.models=? AND a.error='' AND "
variants=[]
for case in [cases[0],cases[2],cases[3]]:
    variants.append((case['name']+'_original',base+case['clause'],[model]+case['args'],case['name']))
variants.append(('all_covering',base.replace('catalog.images i JOIN','catalog.images i INDEXED BY image_person_capture JOIN')+'1=1',[model],cases[0]['name']))
d=cases[2]['args'][0]
variants.append(('directory_existing_covering',"SELECT i.id FROM catalog.images i INDEXED BY image_group_fields_v2 CROSS JOIN ai_images a INDEXED BY ai_status ON a.image_id=i.id AND a.signature=i.signature WHERE i.directory COLLATE NOCASE=? AND i.directory=? AND a.models=? AND a.error=''",[d,d,model],cases[2]['name']))
p=cases[3]['args'][0]
variants.append(('person_simplified_covering',"SELECT DISTINCT i.id FROM faces f INDEXED BY face_person CROSS JOIN ai_images a INDEXED BY ai_status ON a.image_id=f.image_id CROSS JOIN catalog.images i INDEXED BY image_person_capture ON i.id=a.image_id AND i.signature=a.signature WHERE f.person_id=? AND a.models=? AND a.error=''",[p,model],cases[3]['name']))
references={}
results=[]
def measure(label,sql,args,case):
    plan=[x[3] for x in db.execute('EXPLAIN QUERY PLAN '+sql,args)]
    expected=set(row[0] for row in db.execute(sql,args))
    if case in references:
        assert expected==references[case],label
    else:
        references[case]=expected
    times=[]
    for _ in range(3):
        start=time.perf_counter(); rows=db.execute(sql,args).fetchall(); times.append((time.perf_counter()-start)*1000)
        assert {x[0] for x in rows}==expected
    result=dict(name=label,candidates=len(expected),median_ms=statistics.median(times),times_ms=times,plan=plan)
    results.append(result)
    print(label,len(expected),round(result['median_ms'],2),'ms',json.dumps(plan),flush=True)

for v in variants:
    measure(*v)
stats_before={schema:bool(db.execute(f"SELECT 1 FROM {schema}.sqlite_master WHERE name='sqlite_stat1'").fetchone()) for schema in ['main','catalog']}
print('stats_before',stats_before,flush=True)
start=time.perf_counter();db.execute('ANALYZE main');db.execute('ANALYZE catalog');db.commit()
analyze_s=time.perf_counter()-start
for label,sql,args,case in variants[:3]:
    measure(label+'_analyzed',sql,args,case)
start=time.perf_counter()
db.execute('CREATE INDEX IF NOT EXISTS catalog.bench_directory_exact ON images(directory,id,signature)')
db.commit()
index_s=time.perf_counter()-start
measure('directory_new_binary_index',base+cases[2]['clause'],[model]+cases[2]['args'],cases[2]['name'])
db.close()
(HERE/'filter-results.json').write_text(json.dumps(dict(stats_before=stats_before,analyze_s=analyze_s,new_index_s=index_s,results=results),indent=2))
