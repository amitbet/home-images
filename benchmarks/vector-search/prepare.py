"""Create read-only online backups and a fixed, real-data query set."""
import json
import sqlite3
import time
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
DATA = HERE / 'data'
DATA.mkdir(exist_ok=True)
SOURCE = Path('C:/ProgramData/HomeImages')
config = json.loads((SOURCE / 'config.json').read_text(encoding='utf-8-sig'))
for name in ['ai.sqlite3', 'index-go.sqlite3']:
    start = time.perf_counter()
    with sqlite3.connect((SOURCE / name).as_uri() + '?mode=ro', uri=True) as src:
        with sqlite3.connect(DATA / name) as dst:
            src.backup(dst, pages=2048, sleep=0.01)
    print('snapshot', name, round(time.perf_counter() - start, 2), 's', flush=True)

db = sqlite3.connect(DATA / 'ai.sqlite3')
db.execute('ATTACH DATABASE ? AS catalog', (str(DATA / 'index-go.sqlite3'),))
model = config['ml']['clip_model'] + '|' + config['ml']['face_model']
base = "FROM catalog.images i JOIN ai_images a ON a.image_id=i.id AND a.signature=i.signature WHERE a.models=? AND a.error=''"
rows = db.execute('SELECT i.id,a.clip,i.directory ' + base + ' ORDER BY i.id', (model,)).fetchall()
print('eligible vectors', len(rows), 'dimensions', len(rows[0][1]) // 4, flush=True)
directory = db.execute('SELECT i.directory,count(*) ' + base + ' GROUP BY i.directory ORDER BY count(*) DESC LIMIT 1', (model,)).fetchone()[0]
person = db.execute('SELECT f.person_id,count(DISTINCT f.image_id) FROM faces f JOIN ai_images a ON a.image_id=f.image_id JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.models=? AND a.error=\'\' GROUP BY f.person_id ORDER BY count(DISTINCT f.image_id) DESC LIMIT 1', (model,)).fetchone()[0]
cases = [
    dict(name='all_top60', clause='1=1', args=[], k=60),
    dict(name='all_top500', clause='1=1', args=[], k=500),
    dict(name='directory_top60', clause='directory = ?', args=[directory], k=60),
    dict(name='person_top60', clause="id IN(SELECT f.image_id FROM faces f JOIN ai_images ai ON ai.image_id=f.image_id JOIN catalog.images current ON current.id=ai.image_id AND current.signature=ai.signature WHERE f.person_id=? AND ai.models=? AND ai.error='')", args=[person, model], k=60),
]
mapping = {row[0]: j for j, row in enumerate(rows)}
for case in cases:
    ids = [row[0] for row in db.execute('SELECT i.id ' + base + ' AND ' + case['clause'], [model] + case['args'])]
    case['rowids'] = [mapping[x] for x in ids]
    case['eligible'] = len(ids)
    print('case', case['name'], len(ids), flush=True)
with sqlite3.connect(DATA / 'vectors.sqlite3') as vec:
    vec.execute('DROP TABLE IF EXISTS vectors')
    vec.execute('CREATE TABLE vectors(rowid INTEGER PRIMARY KEY, image_id TEXT UNIQUE, embedding BLOB)')
    vec.executemany('INSERT INTO vectors VALUES(?,?,?)', [(j, row[0], row[1]) for j, row in enumerate(rows)])

phrases = ['children playing on a beach', 'a family sitting at a dinner table', 'a dog outdoors', 'snowy mountains', 'a sunset over the sea', 'a birthday cake with candles', 'people dancing at a wedding', 'a city street at night', 'flowers in a garden', 'a baby sleeping', 'a red car', 'food on a plate', 'people swimming in a pool', 'a forest with tall trees', 'a group of friends smiling', 'an old building', 'a person riding a bicycle', 'a cat on a sofa', 'boats on the water', 'a landscape in the desert']
queries = []
for phrase in phrases:
    boundary = 'vector-benchmark-boundary'
    fields = {'text': phrase, 'entries': json.dumps({'clip': {'textual': {'modelName': config['ml']['clip_model'], 'options': {}}}})}
    body = ''.join(f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{val}\r\n' for key, val in fields.items()) + f'--{boundary}--\r\n'
    req = urllib.request.Request(config['ml']['url'].rstrip('/') + '/predict', data=body.encode(), headers={'Content-Type': 'multipart/form-data; boundary=' + boundary})
    start = time.perf_counter()
    with urllib.request.urlopen(req, timeout=180) as response:
        vector = json.load(response)['clip']
    if isinstance(vector, str):
        vector = json.loads(vector)
    queries.append(dict(text=phrase, vector=vector, embedding_ms=(time.perf_counter()-start)*1000))
    print('embedded', len(queries), phrase, flush=True)
(DATA / 'queries.json').write_text(json.dumps(queries))
(DATA / 'cases.json').write_text(json.dumps(cases))
(DATA / 'manifest.json').write_text(json.dumps(dict(model=model, vectors=len(rows), dimensions=len(rows[0][1])//4, created=time.strftime('%Y-%m-%dT%H:%M:%S%z'))))
