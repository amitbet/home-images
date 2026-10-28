"""Read-only post-deployment smoke checks using representative benchmark scopes."""
import json
import sqlite3
import time
import urllib.parse
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
cases = json.loads((HERE/'data/cases.json').read_text())
with sqlite3.connect('file:C:/ProgramData/HomeImages/index-go.sqlite3?mode=ro', uri=True) as db:
    assert db.execute('SELECT 1 FROM sqlite_master WHERE name=?', ('image_directory_exact',)).fetchone()
    assert db.execute('SELECT 1 FROM sqlite_stat1 WHERE idx=?', ('image_directory_exact',)).fetchone()
tests = [
    dict(mode='metadata', directory=cases[2]['args'][0], recursive='false', limit=1),
    dict(mode='metadata', person=cases[3]['args'][0], limit=1),
    dict(mode='semantic', q='children playing on a beach', person=cases[3]['args'][0], limit=1),
    dict(mode='metadata', person=cases[3]['args'][0], group_by='path', limit=1),
]
results = []
for params in tests:
    start = time.perf_counter()
    with urllib.request.urlopen('http://127.0.0.1:8372/api/search?' + urllib.parse.urlencode(params), timeout=60) as response:
        data = json.load(response)
    assert data['items']
    results.append(dict(mode=params['mode'], person='person' in params, directory='directory' in params, grouped='group_by' in params, ms=round((time.perf_counter()-start)*1000,1), total=data['total']))
print(json.dumps(results, indent=2))
(HERE/'data/live-verification.json').write_text(json.dumps(results, indent=2))
