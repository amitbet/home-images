"""Warm-session best case: repeated baseline JPEG at fixed dimensions.
Includes FFmpeg startup in batch wall time, not per output. This is not a
mixed-size photo-library test. Run only after the one-shot benchmark finishes.
"""
import subprocess,time,json,os,io,statistics
from pathlib import Path
from PIL import Image
root=Path(__file__).resolve().parents[1];base=root/'dist/preview-benchmark';source=base/'input/sample-06.jpg';data=source.read_bytes();n=30
rows=[]
for w,h in [(480,270),(1920,1080)]:
 for name in ['ffmpeg-full','ffmpeg-lowres','qsv-scale','qsv-all']:
  args=['ffmpeg','-hide_banner','-loglevel','error','-nostdin','-threads','1']
  gpu=name.startswith('qsv')
  if gpu:args+=['-hwaccel','qsv','-hwaccel_output_format','qsv','-c:v','mjpeg_qsv']
  if name=='ffmpeg-lowres':args+=['-lowres','3' if w==480 else '1']
  args+=['-f','mjpeg','-i','pipe:0']
  filters=[f'scale={w}:{h}:flags=lanczos']
  if gpu:filters=[f'scale_qsv=w={w}:h={h}:format=nv12']
  if name=='qsv-scale':filters+=['hwdownload','format=nv12']
  encoder=['-c:v','mjpeg_qsv','-global_quality','72' if w==480 else '85'] if name=='qsv-all' else ['-c:v','mjpeg','-q:v','4','-pix_fmt','yuvj420p','-threads','1']
  args+=['-vf',','.join(filters),'-frames:v',str(n)]+encoder+['-f','image2pipe','pipe:1']
  runs=[]
  for repeat in range(2):
   start=time.perf_counter();p=subprocess.run(args,input=data*n,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=90);elapsed=time.perf_counter()-start
   if p.returncode:rows.append(dict(method=name,target=f'{w}x{h}',error=p.stderr.decode(errors='replace')[-1200:]));break
   # Fully decode every frame to verify that the pipeline delivered n real JPEGs.
   chunks=p.stdout.split(b'\xff\xd8')[1:];valid=0
   for chunk in chunks:
    try:
     im=Image.open(io.BytesIO(b'\xff\xd8'+chunk));im.load()
     if im.size==(w,h):valid+=1
    except Exception:pass
   if valid!=n:raise RuntimeError(f'{name}: only {valid}/{n} outputs')
   runs.append(dict(total_ms=elapsed*1000,ms_per_image=elapsed*1000/n,bytes_per_image=len(p.stdout)/n))
  if runs:rows.append(dict(method=name,target=f'{w}x{h}',frames=n,runs=runs,median_ms_per_image=statistics.median(r['ms_per_image'] for r in runs)))
  print(name,w,rows[-1],flush=True)
(base/'batch-results.json').write_text(json.dumps(rows,indent=2))
