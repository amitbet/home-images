"""Summarize anonymized timing records; never publishes sample photographs."""
import json,statistics,math,io
from pathlib import Path
from PIL import Image,ImageChops
ROOT=Path(__file__).resolve().parents[1];BASE=ROOT/'dist/preview-benchmark';OUT=BASE/'output';records=json.loads((BASE/'results.json').read_text());batch=json.loads((BASE/'batch-results.json').read_text())
methods=['go-worker','turbo-worker','go','turbo-full','turbo-scaled','pillow-full','pillow-scaled','ffmpeg-full','ffmpeg-lowres','qsv-decode','qsv-scale','qsv-all']
labels={'turbo-worker':'TurboJPEG Go worker (one shot)','go-worker':'Current Go worker (one shot)','go':'Current Go code (persistent)','turbo-full':'TurboJPEG from Go, full decode','turbo-scaled':'TurboJPEG from Go, scaled decode','pillow-full':'Pillow/libjpeg-turbo, full decode','pillow-scaled':'Pillow/libjpeg-turbo, scaled decode','ffmpeg-full':'FFmpeg software, full decode','ffmpeg-lowres':'FFmpeg software, scaled decode','qsv-decode':'QSV decode + CPU resize/encode','qsv-scale':'QSV decode/resize + CPU encode','qsv-all':'QSV decode/resize/rotate/encode'}
summary=[]
for target in ['480x360','1920x1440']:
 for method in methods:
  group=[r for r in records if r['method']==method and r['target']==target and r['sample'].startswith('sample-')];ok=[r for r in group if 'ms'in r];values=sorted(r['ms'] for r in ok)
  row=dict(target=target,method=method,label=labels[method],success=len(ok),attempts=len(group),median_ms=round(statistics.median(values),2) if values else None,p95_ms=round(values[min(len(values)-1,math.ceil(len(values)*.95)-1)],2) if values else None,median_kib=round(statistics.median(r['bytes'] for r in ok)/1024,1) if ok else None)
  psnrs=[]
  for r in ok:
   if r['round']!=0:continue
   w=int(target.split('x')[0]);reference=OUT/f"{r['sample']}.{w}.go.jpg";candidate=OUT/f"{r['sample']}.{w}.{method}.jpg"
   if not reference.exists() or not candidate.exists():continue
   with Image.open(reference) as a,Image.open(candidate) as b:
    a=a.convert('RGB');b=b.convert('RGB').resize(a.size,Image.Resampling.LANCZOS)
    histogram=ImageChops.difference(a,b).histogram();mse=sum((i%256)**2*n for i,n in enumerate(histogram))/(a.width*a.height*3)
    psnrs.append(10*math.log10(255**2/mse) if mse else 99)
  row['median_psnr_vs_go']=round(statistics.median(psnrs),2) if psnrs else None
  summary.append(row)
(BASE/'summary-final.json').write_text(json.dumps(summary,indent=2))
report=['# N150 image preview benchmark — 2026-10-26','',
'## Result','',
'Scaled TurboJPEG is the best immediate integration candidate: median one-shot thumbnail generation fell from 599 to 156 ms (3.8x), and viewer generation from 1078 to 419 ms (2.6x). A persistent native path achieved 52 ms thumbnails and 280 ms viewers. Persistent QSV is promising for background batch work (45 ms thumbnails / 51 ms viewers in the fixed-size test), but not one-process-per-request serving. No production decoder/configuration was changed. All generated samples and dependency binaries remain under ignored `dist/`; source photos were read-only.','',
'## Setup and scope','',
'- Intel N150 (4 cores/4 threads), Windows, Intel graphics driver 32.0.101.7088; installed FFmpeg 8.1.2 essentials; libjpeg-turbo 3.2.0 official vc-x64 DLL; Pillow 13.0.0 with libjpeg-turbo 3.2.0.',
'- Eight library JPEGs, two each in four pixel-count bands: 0.63–16.08 MP. Includes EXIF orientations 1, 6, and 8. Two additional 16 MP derivatives test progressive JPEG and orientation 6.',
'- Two randomized rounds × 10 inputs × 2 target sizes × 11 methods = 440 one-shot/persistent-library measurements, plus 32 native Go worker measurements (472 total). Tables use the same eight real photographs (16 timings per method/target); synthetic compatibility cases are separate.',
'- Targets: fit within 480×360 at quality 72, and 1920×1440 at quality 85; no upscaling. Native Go/TurboJPEG and Pillow use equivalent quality settings, 4:2:0 output and Lanczos final resize. FFmpeg software encoding uses q:v=4, QSV encoding uses global_quality=72/85: these are not identical quality scales. Output size and PSNR help expose differences; this is not a matched-rate/distortion study.',
'- OS file caches warm; output cache bypassed. Read, decode, orient, resize and JPEG encode included. Validation/output saving excluded from library timings. The native one-shot Go benchmark worker includes a temporary JPEG file write and test-harness startup; current app worker uses stdout, so that row is a conservative prototype comparison. Go worker, native Go worker and FFmpeg rows include process startup; Go/TurboJPEG/Pillow library rows reuse a running process. Go rows include the existing metadata parse, CPU cap GOMAXPROCS=1 and GOMEMLIMIT=256MiB; Pillow/FFmpeg do not perform the full application metadata parse.',
'- Production service and background indexing remained running. Two randomized rounds reduce but do not eliminate contention/thermal noise. This is a practical local benchmark, not an idle-machine laboratory result.',
'- Native TurboJPEG was called directly from a Go benchmark adapter using Windows DLL procedures (no CGO), with the existing Go orientation/Lanczos pipeline. This validates the integration approach rather than estimating from another language.',
'- Sample 04 has a recoverable invalid sequential SOS parameter warning. A strict first pass rejected it; the Go adapter was corrected to distinguish warnings from fatal errors. Only its failed TurboJPEG measurements were rerun, and the resulting outputs decoded successfully. Original strict records are retained in ignored dist/.','']
for target in ['480x360','1920x1440']:
 report+=['## '+target+' — real-photo results','', '| Method | Median ms | P95 ms | Median KiB | PSNR vs Go, dB | Success |','|---|---:|---:|---:|---:|---:|']
 for r in summary:
  if r['target']==target:report.append(f"| {r['label']} | {r['median_ms']} | {r['p95_ms']} | {r['median_kib']} | {r['median_psnr_vs_go']} | {r['success']}/{r['attempts']} |")
 report+=['','PSNR compares resized decoded output to the current Go result; it is a difference metric, not proof of better/worse perceived quality. Small rounding differences are normalized to the reference size.','']
report+=['## Fixed-size warm pipeline (best case)','',
'Thirty repeats of the same 9.14 MP baseline JPEG in each FFmpeg invocation, two runs per method. This amortizes device/process initialization and tests persistent decode/resize/encode throughput; all 30 output JPEGs were decoded and dimensions checked. It is not a mixed-resolution library test or per-request latency. Timings still include process startup divided over the batch.','', '| Method | Output | Median ms/image |','|---|---|---:|']
for row in batch:
 report.append(f"| {row['method']} | {row['target']} | {round(row['median_ms_per_image'],2) if 'median_ms_per_image' in row else 'FAILED'} |")
report+=['','## Compatibility and deployment implications','',
'- For the same 9.14 MP batch source, the persistent native Go TurboJPEG measurements were 54 ms/thumbnail and 302 ms/viewer; Go was 840/1042 ms. The warm QSV batch is thus promising for large preview generation, although output quality, batch behavior and startup differ.',
'- Tested QSV paths reject the progressive JPEG; software fallback is required. See raw results for per-method errors. Rotation was checked against dimensions and visually for sample 05; QSV VPP scales before rotating, so swapped dimensions were explicitly handled.',
'- Native scaled TurboJPEG and software methods accept the progressive case, but it is slower than baseline: scaled output does not remove all progressive coefficient processing.',
'- The current service uses two foreground decoder slots. Persistent worker/session design can remove setup costs, but heterogeneous JPEG sizes may require GPU session reconfiguration; the repeated-image batch is an upper-bound scenario.',
'- QSV was tested from the current interactive Windows account, not the LocalService service account. Service-account GPU access needs verification before deployment.',
'- JPEG only: no claims for HEIC, PNG, TIFF, corrupt-file robustness, or full-library coverage. Eight real photos is enough to choose a prototype, not remove fallbacks.',
'- The native Go DLL adapter is benchmark-only. A production adapter needs safe buffer limits, DLL loading policy, warning handling, timeout/isolation behavior, dependency packaging and tests for all supported platforms.','',
'## Reproduction','',
'1. Create `dist/bench-venv` and install Pillow; extract the official libjpeg-turbo 3.2.0 vc-x64 release into `dist/turbojpeg` using 7-Zip.',
'2. Run `benchmarks/prepare_preview_benchmark.py` (uses the local read-only catalog and copies selected inputs into dist/preview-benchmark/input).',
'3. Compile `go test -tags previewbench -c -o dist/preview-benchmark.exe .`; have the regular app binary at dist/home-images-windows-amd64.exe.',
'4. Run `benchmarks/preview_benchmark.py`, then `benchmarks/preview_batch_benchmark.py`, then `benchmarks/preview_native_worker_benchmark.py` sequentially with the benchmark venv Python. The warning-aware bridge now handles recoverable warnings on the first run; the original run required `--retry-warnings`.',
'5. Run `benchmarks/analyze_preview_benchmark.py` for this report and HTML summary.',
'', 'Raw timings: `dist/preview-benchmark/results.json`; final aggregates: `dist/preview-benchmark/summary-final.json`; batch timings: `dist/preview-benchmark/batch-results.json`. These contain anonymized sample identifiers. The private manifest and sample/output images stay in ignored dist/.',
'', '## Sources','',
'- https://github.com/libjpeg-turbo/libjpeg-turbo/releases/tag/3.2.0',
'- https://github.com/libjpeg-turbo/libjpeg-turbo/blob/main/src/turbojpeg.h',
'- https://ffmpeg.org/ffmpeg-codecs.html',
'- https://pillow.readthedocs.io/en/stable/handbook/image-file-formats.html#jpeg','']
(ROOT/'benchmarks/n150-preview-results-2026-10-26.md').write_text('\n'.join(report),encoding='utf-8')
# Inline table with selectable target; no photographs or private paths.
public=[{k:v for k,v in row.items() if k not in ('median_psnr_vs_go',)} for row in summary]
html='''<!doctype html><html><head><meta charset="utf-8"><style>
body{font-family:var(--font-sans,system-ui);color:var(--foreground,#222);margin:0}h2{font-size:21px;margin:0 0 8px}p{color:var(--muted-foreground,#666);font-size:13px;line-height:1.5}select{font:inherit;padding:8px;background:var(--card,#fff);color:inherit;border:1px solid var(--border,#ddd);border-radius:6px}table{width:100%;border-collapse:collapse;font-size:13px;margin-top:15px}th,td{padding:9px 6px;text-align:right;border-bottom:1px solid var(--border,#ddd)}th:first-child,td:first-child{text-align:left}tr.best{background:var(--accent-surface,#eaf4ec)}.wrap{overflow-x:auto}small{font-size:12px;color:var(--muted-foreground,#666)}
</style></head><body><h2>N150: measured image generation</h2><p>Eight real photos · 0.63–16 MP · two rounds · cached outputs bypassed</p><label>Preview size <select id="size"><option value="480x360">Grid thumbnail — 480×360</option><option value="1920x1440">Viewer — 1920×1440</option></select></label><div class="wrap"><table><thead><tr><th>Method</th><th>Median</th><th>P95</th><th>KiB</th></tr></thead><tbody id="rows"></tbody></table></div><p>Both Go worker rows and FFmpeg/QSV include startup per image. Library rows reuse a process. Encoding quality scales differ; these are practical pipeline comparisons, not matched-quality codec rankings.</p><p><strong>Persistent GPU batch:</strong> 45 ms per thumbnail and 51 ms per viewer image. Best-case throughput: 30 repeats of one 9 MP JPEG per session, not mixed-library request latency.</p><small>Quick Sync failed the additional progressive JPEG test. Production indexing stayed running. No production decoder was changed.</small><script>const data=DATA;const size=document.getElementById('size');function render(){const rows=data.filter(r=>r.target===size.value);document.getElementById('rows').innerHTML=rows.map(r=>`<tr class="${r.method==='turbo-scaled'?'best':''}"><td>${r.label}</td><td>${Math.round(r.median_ms)} ms</td><td>${Math.round(r.p95_ms)} ms</td><td>${r.median_kib}</td></tr>`).join('');}document.getElementById('size').onchange=render;render();console.log('Rendered',data.length,'method/size aggregates');</script></body></html>'''.replace('DATA',json.dumps(public))
(ROOT/'benchmarks/n150-preview-results-2026-10-26.html').write_text(html,encoding='utf-8')
print(json.dumps(summary,indent=2));print('Batch',json.dumps(batch,indent=2))
