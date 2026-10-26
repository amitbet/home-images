import sqlite3,json,random,shutil
from pathlib import Path
from PIL import Image
base=Path('dist/preview-benchmark'); inp=base/'input';inp.mkdir(parents=True,exist_ok=True)
rows=sqlite3.connect('file:C:/ProgramData/HomeImages/index-go.sqlite3?mode=ro',uri=True).execute('select metadata from images order by id limit 3000').fetchall()
random.Random(150).shuffle(rows);buckets=[[],[],[],[]]
for (raw,) in rows:
 p=json.loads(raw)
 if p.get('format','').upper() not in ('JPEG','JPG'):continue
 mp=p['width']*p['height']/1e6
 bucket=0 if mp<2 else 1 if mp<6 else 2 if mp<12 else 3
 if len(buckets[bucket])>=2:continue
 path=Path(p['absolute_path'])
 try:
  with Image.open(path) as im:
   if im.mode not in ('RGB','L'):continue
   meta=dict(width=im.width,height=im.height,orientation=im.getexif().get(274,1),progressive=bool(im.info.get('progressive') or im.info.get('progression')))
  buckets[bucket].append((path,meta))
 except Exception:pass
 if all(len(x)==2 for x in buckets):break
samples=[]
for path,meta in sum(buckets,[]):
 name=f'sample-{len(samples)+1:02}.jpg';shutil.copyfile(path,inp/name);samples.append(dict(name=name,source=str(path),**meta))
# Exercise features hardware JPEG decoders often restrict, without changing originals.
source=max(samples,key=lambda p:p['width']*p['height'])
with Image.open(inp/source['name']) as im:
 im=im.convert('RGB');im.save(inp/'progressive.jpg',quality=90,progressive=True)
 samples.append(dict(name='progressive.jpg',source='synthetic derivative',width=im.width,height=im.height,orientation=1,progressive=True))
 exif=im.getexif();exif[274]=6;im.save(inp/'rotated.jpg',quality=90,exif=exif)
 samples.append(dict(name='rotated.jpg',source='synthetic derivative',width=im.width,height=im.height,orientation=6,progressive=False))
(base/'samples-private.json').write_text(json.dumps(samples,indent=2))
print(json.dumps([{k:v for k,v in s.items() if k!='source'} for s in samples],indent=2))
