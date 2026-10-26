# N150 image preview benchmark — 2026-10-26

## Result

Scaled TurboJPEG is the best immediate integration candidate: median one-shot thumbnail generation fell from 599 to 156 ms (3.8x), and viewer generation from 1078 to 419 ms (2.6x). A persistent native path achieved 52 ms thumbnails and 280 ms viewers. Persistent QSV is promising for background batch work (45 ms thumbnails / 51 ms viewers in the fixed-size test), but not one-process-per-request serving. No production decoder/configuration was changed. All generated samples and dependency binaries remain under ignored `dist/`; source photos were read-only.

## Setup and scope

- Intel N150 (4 cores/4 threads), Windows, Intel graphics driver 32.0.101.7088; installed FFmpeg 8.1.2 essentials; libjpeg-turbo 3.2.0 official vc-x64 DLL; Pillow 13.0.0 with libjpeg-turbo 3.2.0.
- Eight library JPEGs, two each in four pixel-count bands: 0.63–16.08 MP. Includes EXIF orientations 1, 6, and 8. Two additional 16 MP derivatives test progressive JPEG and orientation 6.
- Two randomized rounds × 10 inputs × 2 target sizes × 11 methods = 440 one-shot/persistent-library measurements, plus 32 native Go worker measurements (472 total). Tables use the same eight real photographs (16 timings per method/target); synthetic compatibility cases are separate.
- Targets: fit within 480×360 at quality 72, and 1920×1440 at quality 85; no upscaling. Native Go/TurboJPEG and Pillow use equivalent quality settings, 4:2:0 output and Lanczos final resize. FFmpeg software encoding uses q:v=4, QSV encoding uses global_quality=72/85: these are not identical quality scales. Output size and PSNR help expose differences; this is not a matched-rate/distortion study.
- OS file caches warm; output cache bypassed. Read, decode, orient, resize and JPEG encode included. Validation/output saving excluded from library timings. The native one-shot Go benchmark worker includes a temporary JPEG file write and test-harness startup; current app worker uses stdout, so that row is a conservative prototype comparison. Go worker, native Go worker and FFmpeg rows include process startup; Go/TurboJPEG/Pillow library rows reuse a running process. Go rows include the existing metadata parse, CPU cap GOMAXPROCS=1 and GOMEMLIMIT=256MiB; Pillow/FFmpeg do not perform the full application metadata parse.
- Production service and background indexing remained running. Two randomized rounds reduce but do not eliminate contention/thermal noise. This is a practical local benchmark, not an idle-machine laboratory result.
- Native TurboJPEG was called directly from a Go benchmark adapter using Windows DLL procedures (no CGO), with the existing Go orientation/Lanczos pipeline. This validates the integration approach rather than estimating from another language.
- Sample 04 has a recoverable invalid sequential SOS parameter warning. A strict first pass rejected it; the Go adapter was corrected to distinguish warnings from fatal errors. Only its failed TurboJPEG measurements were rerun, and the resulting outputs decoded successfully. Original strict records are retained in ignored dist/.

## 480x360 — real-photo results

| Method | Median ms | P95 ms | Median KiB | PSNR vs Go, dB | Success |
|---|---:|---:|---:|---:|---:|
| Current Go worker (one shot) | 599.11 | 2177.21 | 22.7 | 99.0 | 16/16 |
| TurboJPEG Go worker (one shot) | 155.81 | 353.87 | 21.9 | 36.18 | 16/16 |
| Current Go code (persistent) | 597.12 | 1970.53 | 22.7 | 99.0 | 16/16 |
| TurboJPEG from Go, full decode | 330.6 | 1426.61 | 22.5 | 41.18 | 16/16 |
| TurboJPEG from Go, scaled decode | 52.01 | 92.76 | 21.9 | 36.18 | 16/16 |
| Pillow/libjpeg-turbo, full decode | 249.07 | 508.27 | 22.6 | 42.04 | 16/16 |
| Pillow/libjpeg-turbo, scaled decode | 53.7 | 147.17 | 22.4 | 40.09 | 16/16 |
| FFmpeg software, full decode | 267.64 | 698.5 | 23.0 | 33.23 | 16/16 |
| FFmpeg software, scaled decode | 221.59 | 531.46 | 22.9 | 32.74 | 16/16 |
| QSV decode + CPU resize/encode | 1081.64 | 2711.8 | 24.8 | 27.26 | 16/16 |
| QSV decode/resize + CPU encode | 1024.98 | 4502.05 | 25.1 | 24.4 | 16/16 |
| QSV decode/resize/rotate/encode | 1125.33 | 2703.78 | 22.5 | 28.23 | 16/16 |

PSNR compares resized decoded output to the current Go result; it is a difference metric, not proof of better/worse perceived quality. Small rounding differences are normalized to the reference size.

## 1920x1440 — real-photo results

| Method | Median ms | P95 ms | Median KiB | PSNR vs Go, dB | Success |
|---|---:|---:|---:|---:|---:|
| Current Go worker (one shot) | 1077.82 | 2536.34 | 234.4 | 99.0 | 16/16 |
| TurboJPEG Go worker (one shot) | 418.59 | 661.31 | 231.9 | 42.03 | 16/16 |
| Current Go code (persistent) | 1042.2 | 2139.99 | 234.4 | 99.0 | 16/16 |
| TurboJPEG from Go, full decode | 682.38 | 1682.94 | 233.8 | 44.21 | 16/16 |
| TurboJPEG from Go, scaled decode | 279.63 | 997.29 | 231.9 | 42.03 | 16/16 |
| Pillow/libjpeg-turbo, full decode | 284.31 | 858.09 | 233.8 | 45.07 | 16/16 |
| Pillow/libjpeg-turbo, scaled decode | 288.01 | 698.72 | 233.8 | 45.07 | 16/16 |
| FFmpeg software, full decode | 318.98 | 950.58 | 145.5 | 39.23 | 16/16 |
| FFmpeg software, scaled decode | 266.29 | 545.77 | 145.3 | 39.09 | 16/16 |
| QSV decode + CPU resize/encode | 1273.92 | 2792.02 | 158.4 | 27.85 | 16/16 |
| QSV decode/resize + CPU encode | 1009.41 | 3268.45 | 152.9 | 27.25 | 16/16 |
| QSV decode/resize/rotate/encode | 1037.04 | 2102.27 | 221.4 | 36.82 | 16/16 |

PSNR compares resized decoded output to the current Go result; it is a difference metric, not proof of better/worse perceived quality. Small rounding differences are normalized to the reference size.

## Fixed-size warm pipeline (best case)

Thirty repeats of the same 9.14 MP baseline JPEG in each FFmpeg invocation, two runs per method. This amortizes device/process initialization and tests persistent decode/resize/encode throughput; all 30 output JPEGs were decoded and dimensions checked. It is not a mixed-resolution library test or per-request latency. Timings still include process startup divided over the batch.

| Method | Output | Median ms/image |
|---|---|---:|
| ffmpeg-full | 480x270 | 94.87 |
| ffmpeg-lowres | 480x270 | 72.33 |
| qsv-scale | 480x270 | 51.42 |
| qsv-all | 480x270 | 44.79 |
| ffmpeg-full | 1920x1080 | 84.7 |
| ffmpeg-lowres | 1920x1080 | 89.53 |
| qsv-scale | 1920x1080 | 67.92 |
| qsv-all | 1920x1080 | 51.01 |

## Compatibility and deployment implications

- For the same 9.14 MP batch source, the persistent native Go TurboJPEG measurements were 54 ms/thumbnail and 302 ms/viewer; Go was 840/1042 ms. The warm QSV batch is thus promising for large preview generation, although output quality, batch behavior and startup differ.
- Tested QSV paths reject the progressive JPEG; software fallback is required. See raw results for per-method errors. Rotation was checked against dimensions and visually for sample 05; QSV VPP scales before rotating, so swapped dimensions were explicitly handled.
- Native scaled TurboJPEG and software methods accept the progressive case, but it is slower than baseline: scaled output does not remove all progressive coefficient processing.
- The current service uses two foreground decoder slots. Persistent worker/session design can remove setup costs, but heterogeneous JPEG sizes may require GPU session reconfiguration; the repeated-image batch is an upper-bound scenario.
- QSV was tested from the current interactive Windows account, not the LocalService service account. Service-account GPU access needs verification before deployment.
- JPEG only: no claims for HEIC, PNG, TIFF, corrupt-file robustness, or full-library coverage. Eight real photos is enough to choose a prototype, not remove fallbacks.
- The native Go DLL adapter is benchmark-only. A production adapter needs safe buffer limits, DLL loading policy, warning handling, timeout/isolation behavior, dependency packaging and tests for all supported platforms.

## Reproduction

1. Create `dist/bench-venv` and install Pillow; extract the official libjpeg-turbo 3.2.0 vc-x64 release into `dist/turbojpeg` using 7-Zip.
2. Run `benchmarks/prepare_preview_benchmark.py` (uses the local read-only catalog and copies selected inputs into dist/preview-benchmark/input).
3. Compile `go test -tags previewbench -c -o dist/preview-benchmark.exe .`; have the regular app binary at dist/home-images-windows-amd64.exe.
4. Run `benchmarks/preview_benchmark.py`, then `benchmarks/preview_batch_benchmark.py`, then `benchmarks/preview_native_worker_benchmark.py` sequentially with the benchmark venv Python. The warning-aware bridge now handles recoverable warnings on the first run; the original run required `--retry-warnings`.
5. Run `benchmarks/analyze_preview_benchmark.py` for this report and HTML summary.

Raw timings: `dist/preview-benchmark/results.json`; final aggregates: `dist/preview-benchmark/summary-final.json`; batch timings: `dist/preview-benchmark/batch-results.json`. These contain anonymized sample identifiers. The private manifest and sample/output images stay in ignored dist/.

## Sources

- https://github.com/libjpeg-turbo/libjpeg-turbo/releases/tag/3.2.0
- https://github.com/libjpeg-turbo/libjpeg-turbo/blob/main/src/turbojpeg.h
- https://ffmpeg.org/ffmpeg-codecs.html
- https://pillow.readthedocs.io/en/stable/handbook/image-file-formats.html#jpeg
