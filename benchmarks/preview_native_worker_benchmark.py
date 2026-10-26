"""One process per image, using the same no-CGO TurboJPEG Go bridge."""
import json,subprocess,os,time,random,io
from pathlib import Path
from PIL import Image
root=Path(__file__).resolve().parents[1];base=root/'dist/preview-benchmark';out=base/'output';samples=[s for s in json.loads((base/'samples-private.json').read_text()) if s['name'].startswith('sample-')]
env=dict(os.environ,GOMAXPROCS='1',GOMEMLIMIT='256MiB',HOMEIMAGES_BENCH_ROOT=str(base/'input'),HOMEIMAGES_BENCH_TURBO_DLL=str(root/'dist/turbojpeg/bin/turbojpeg.dll'))
records=[r for r in json.loads((base/'results.json').read_text()) if r['method']!='turbo-worker']
jobs=[(repeat,s,w,h,q) for repeat in range(2) for s in samples for w,h,q in [(480,360,72),(1920,1440,85)]];random.Random(150).shuffle(jobs)
for repeat,s,w,h,q in jobs:
 output=out/'native-worker.jpg';request=dict(Method='turbo-scaled',Path=s['name'],Width=w,Height=h,Quality=q,Output=str(output))
 start=time.perf_counter();p=subprocess.run([str(root/'dist/preview-benchmark.exe'),'-test.run=^TestPreviewBenchmarkBridge$','-test.timeout=30s'],input=json.dumps(request)+'\n',text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env,timeout=40);ms=(time.perf_counter()-start)*1000
 result=json.loads(next(line[6:] for line in p.stdout.splitlines() if line.startswith('BENCH ')))
 if p.returncode or result['Error']:raise RuntimeError(p.stdout+p.stderr)
 with Image.open(output) as im:im.load();ow,oh=im.size
 record=dict(round=repeat,sample=s['name'],mp=s['width']*s['height']/1e6,orientation=s['orientation'],progressive=s['progressive'],target=f'{w}x{h}',method='turbo-worker',ms=ms,library_ms=result['MS'],bytes=result['Bytes'],width=ow,height=oh)
 records.append(record)
 if repeat==0:(out/f"{s['name']}.{w}.turbo-worker.jpg").write_bytes(output.read_bytes())
 print(s['name'],w,round(ms,2),flush=True)
(base/'results.json').write_text(json.dumps(records,indent=2))
