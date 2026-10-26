"""Manual Windows/N150 benchmark. Originals are copied to ignored dist/ before use.
Requires Pillow for validation; native library calls run through the Go bridge.
No service/database writes or network image requests during timed sections.
"""
import os,sys,json,time,subprocess,random,math,statistics,io
from pathlib import Path
from PIL import Image,ImageOps,features
ROOT=Path(__file__).resolve().parents[1];BASE=ROOT/'dist/preview-benchmark';INPUT=BASE/'input';OUT=BASE/'output';OUT.mkdir(exist_ok=True)
samples=json.loads((BASE/'samples-private.json').read_text())
DLL=ROOT/'dist/turbojpeg/bin/turbojpeg.dll'
env=dict(os.environ,GOMAXPROCS='1',GOMEMLIMIT='256MiB',HOMEIMAGES_BENCH_ROOT=str(INPUT),HOMEIMAGES_BENCH_TURBO_DLL=str(DLL))
bridge=subprocess.Popen([str(ROOT/'dist/preview-benchmark.exe'),'-test.run=^TestPreviewBenchmarkBridge$','-test.timeout=30m'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,env=env)
def go(method,s,w,h,q):
 request=dict(Method=method,Path=s['name'],Width=w,Height=h,Quality=q,Output=str(OUT/'bridge.jpg'))
 bridge.stdin.write(json.dumps(request)+'\n');bridge.stdin.flush()
 while True:
  line=bridge.stdout.readline()
  if not line:raise RuntimeError('Go bridge exited: '+bridge.stderr.read())
  if line.startswith('BENCH '):break
 result=json.loads(line[6:]);
 if result['Error']:raise RuntimeError(result['Error'])
 return (OUT/'bridge.jpg').read_bytes(),result['MS']
def dims(s,w,h):
 iw,ih=s['width'],s['height']
 if s['orientation'] in (5,6,7,8):iw,ih=ih,iw
 ratio=min(1,w/iw,h/ih)
 return max(2,int(iw*ratio)//2*2),max(2,int(ih*ratio)//2*2)
def ffmpeg(method,s,w,h,q):
 tw,th=dims(s,w,h);sw,sh=(th,tw) if s['orientation'] in (5,6,7,8) else (tw,th)
 args=['ffmpeg','-hide_banner','-loglevel','error','-nostdin','-threads','1']
 gpu=method.startswith('qsv')
 if method=='qsv-all':args+=['-init_hw_device','qsv=hw','-filter_hw_device','hw']
 if gpu:args+=['-hwaccel','qsv','-hwaccel_output_format','qsv','-c:v','mjpeg_qsv']
 if method=='ffmpeg-lowres':
  lowres=max(0,min(3,int(math.log2(min(s['width']/sw,s['height']/sh)))))
  args+=['-lowres',str(lowres)]
 args+=['-noautorotate','-i',str(INPUT/s['name'])]
 filters=[]
 if gpu:
  if method in ('qsv-scale','qsv-all'):filters += [f'scale_qsv=w={sw}:h={sh}:format=nv12']
  filters+=['hwdownload','format=nv12']
 rotation={2:['hflip'],3:['hflip','vflip'],4:['vflip'],5:['transpose=1','hflip'],6:['transpose=1'],7:['transpose=1','vflip'],8:['transpose=2']}.get(s['orientation'],[])
 filters+=rotation
 if method not in ('qsv-scale','qsv-all'):filters += [f'scale={tw}:{th}:flags=lanczos']
 if method=='qsv-all':
  filters+=['format=nv12','hwupload=extra_hw_frames=16']
  encoder=['-c:v','mjpeg_qsv','-global_quality',str(q)]
 else:encoder=['-c:v','mjpeg','-q:v','4','-pix_fmt','yuvj420p','-threads','1']
 if method=='qsv-all':
  transpose={2:'hflip',3:'reversal',4:'vflip',5:'cclock_hflip',6:'clock',7:'clock_hflip',8:'cclock'}.get(s['orientation'])
  filters=[f'vpp_qsv=w={sw}:h={sh}:format=nv12'+(':'+f'transpose={transpose}' if transpose else '')]
 args+=['-vf',','.join(filters),'-frames:v','1']+encoder+['-f','image2pipe','pipe:1']
 start=time.perf_counter();p=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=30);ms=(time.perf_counter()-start)*1000
 if p.returncode:raise RuntimeError(p.stderr.decode(errors='replace')[-1800:])
 return p.stdout,ms
config=dict(root=str(INPUT),data_dir=str(BASE),max_pixels=80000000,ffmpeg='ffmpeg')
def run(method,s,w,h,q):
 if method in ('go','turbo-full','turbo-scaled'):return go(method,s,w,h,q)
 if method=='go-worker':
  start=time.perf_counter();p=subprocess.run([str(ROOT/'dist/home-images-windows-amd64.exe'),'--ai-preview',s['name']],input=json.dumps(dict(config,preview_width=w,preview_height=h,preview_quality=q)).encode(),stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env,timeout=30);ms=(time.perf_counter()-start)*1000
  if p.returncode:raise RuntimeError(p.stderr.decode(errors='replace'))
  return p.stdout,ms
 if method.startswith('pillow'):
  start=time.perf_counter()
  with Image.open(INPUT/s['name']) as im:
   if method=='pillow-scaled':im.draft('RGB',(h,w) if s['orientation'] in (5,6,7,8) else (w,h))
   im.load();im=ImageOps.exif_transpose(im);im.thumbnail((w,h),Image.Resampling.LANCZOS,reducing_gap=None)
   out=io.BytesIO();im.convert('RGB').save(out,'JPEG',quality=q,subsampling=2)
  return out.getvalue(),(time.perf_counter()-start)*1000
 return ffmpeg(method,s,w,h,q)
methods=['go-worker','go','turbo-full','turbo-scaled','pillow-full','pillow-scaled','ffmpeg-full','ffmpeg-lowres','qsv-decode','qsv-scale','qsv-all']
if '--retry-warnings' in sys.argv:
 records=json.loads((BASE/'results.json').read_text())
 (BASE/'strict-warning-results.json').write_text(json.dumps(records,indent=2))
 for record in records:
  if record.get('error') and record['method'].startswith('turbo') and record['sample']=='sample-04.jpg':
   sample=next(s for s in samples if s['name']==record['sample']);w,h=map(int,record['target'].split('x'));q=72 if w==480 else 85
   data,ms=run(record['method'],sample,w,h,q)
   with Image.open(io.BytesIO(data)) as im:im.load();ow,oh=im.size
   record.pop('error');record.update(ms=ms,bytes=len(data),width=ow,height=oh,warning='Recovered invalid SOS parameters for sequential JPEG')
   if record['round']==0:(OUT/f"{sample['name']}.{w}.{record['method']}.jpg").write_bytes(data)
   print('recovered',record['sample'],record['method'],record['target'],round(ms,2),flush=True)
 (BASE/'results.json').write_text(json.dumps(records,indent=2))
 bridge.stdin.close();bridge.wait();sys.exit()
if '--smoke' in sys.argv:
 for method in methods:
  try:
   data,ms=run(method,samples[4],480,360,72)
   im=Image.open(io.BytesIO(data));im.load();print(method,round(ms,2),im.size,len(data),flush=True)
  except Exception as e:print(method,'ERROR',str(e),flush=True)
 bridge.stdin.close();bridge.wait();sys.exit()
records=[];rng=random.Random(150)
# Warm code/libraries/driver once. OS file caches are intentionally warm.
for method in methods:
 try:run(method,samples[0],480,360,72)
 except Exception:pass
for roundno in range(2):
 jobs=[(s,w,h,q,method) for s in samples for w,h,q in [(480,360,72),(1920,1440,85)] for method in methods]
 rng.shuffle(jobs)
 for i,(s,w,h,q,method) in enumerate(jobs):
  record=dict(round=roundno,sample=s['name'],mp=s['width']*s['height']/1e6,orientation=s['orientation'],progressive=s['progressive'],target=f'{w}x{h}',method=method)
  try:
   data,ms=run(method,s,w,h,q)
   with Image.open(io.BytesIO(data)) as im:
    im.load();ow,oh=im.size
    if ow>w or oh>h:raise RuntimeError(f'Output too large {ow}x{oh}')
    if s['orientation'] in (5,6,7,8) and s['width']>s['height'] and ow>oh:raise RuntimeError('Orientation was not applied')
   record.update(ms=ms,bytes=len(data),width=ow,height=oh)
   if roundno==0:(OUT/f"{s['name']}.{w}.{method}.jpg").write_bytes(data)
  except Exception as e:record['error']=str(e)
  records.append(record)
  (BASE/'results.json').write_text(json.dumps(records,indent=2))
  if (i+1)%22==0:print(f'round {roundno+1}: {i+1}/{len(jobs)}',flush=True)
bridge.stdin.close();bridge.wait(timeout=15)
summary=[]
for target in ['480x360','1920x1440']:
 for method in methods:
  group=[r for r in records if r['method']==method and r['target']==target];ok=[r for r in group if 'ms'in r];values=sorted(r['ms'] for r in ok)
  summary.append(dict(target=target,method=method,success=len(ok),attempts=len(group),median_ms=statistics.median(values) if values else None,p95_ms=values[min(len(values)-1,math.ceil(len(values)*.95)-1)] if values else None,mean_bytes=round(statistics.mean(r['bytes'] for r in ok)) if ok else None))
(BASE/'summary.json').write_text(json.dumps(summary,indent=2));print(json.dumps(summary,indent=2))
