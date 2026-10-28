# Vector search benchmark — 2026-10-27

Intel N150 · Windows amd64 · 58,778 real CLIP vectors · 512 dimensions · 20 text queries.

All numbers are retrieval-only warm-search measurements. Production code and deployment are unchanged.

| Case | Candidates | Go exact median / p95 ms | Native SQLite exact median / p95 ms |
|---|---:|---:|---:|
| all_top60 | 58,778 | 1546.97 / 2502.73 | 975.98 / 1416.18 |
| all_top500 | 58,778 | 1569.68 / 2083.10 | 978.34 / 1108.00 |
| directory_top60 | 3,124 | 1912.61 / 2489.68 | 1688.79 / 2108.70 |
| person_top60 | 5,091 | 1946.81 / 2375.05 | 1846.48 / 2242.94 |

| Case | ef | HNSW median / p95 ms | Mean recall | Worst query recall | Speedup vs Go median |
|---|---:|---:|---:|---:|---:|
| all_top60 | 100 | 1.076 / 1.590 | 91.67% | 63.33% | 1438× |
| all_top60 | 500 | 3.100 / 4.921 | 98.67% | 91.67% | 499× |
| all_top60 | 1000 | 4.496 / 6.728 | 99.58% | 96.67% | 344× |
| all_top60 | 2000 | 7.338 / 9.573 | 99.75% | 96.67% | 211× |
| all_top500 | 100 | 12.918 / 15.964 | 94.16% | 86.20% | 122× |
| all_top500 | 500 | 12.373 / 14.240 | 94.16% | 86.20% | 127× |
| all_top500 | 1000 | 12.954 / 18.131 | 97.92% | 92.80% | 121× |
| all_top500 | 2000 | 18.573 / 26.918 | 99.37% | 97.00% | 85× |
| directory_top60 | 100 | 6.699 / 11.237 | 98.17% | 91.67% | 286× |
| directory_top60 | 500 | 24.608 / 38.575 | 99.92% | 98.33% | 78× |
| directory_top60 | 1000 | 40.889 / 59.031 | 100.00% | 100.00% | 47× |
| directory_top60 | 2000 | 63.046 / 94.368 | 100.00% | 100.00% | 30× |
| person_top60 | 100 | 14.010 / 28.228 | 97.50% | 85.00% | 139× |
| person_top60 | 500 | 23.934 / 38.577 | 99.67% | 96.67% | 81× |
| person_top60 | 1000 | 33.763 / 51.443 | 99.92% | 98.33% | 58× |
| person_top60 | 2000 | 59.454 / 138.850 | 99.92% | 98.33% | 33× |

Filtered HNSW timings above use cached candidate rowids. Resolving those rowids from current metadata adds:

- directory_top60: median 1667.59 ms, p95 1797.60 ms (5 samples).
- person_top60: median 1818.71 ms, p95 1894.52 ms (5 samples).

Build: 72.65 s. Index: 123.12 MiB. Approximate process working-set increase while building: 136.75 MiB. Save: 1.85 s. Warm-OS-cache reload: 0.54 s.

Extension: vectorlite extension version 0.2.0, built with SSE. Native SQLite 3.50.4. Go uses modernc.org/sqlite v1.61.0.

The native exact control retains an exhaustive scan and the original catalog/model/signature joins. HNSW uses the prevalidated snapshot index and a separate ID lookup. This comparison therefore includes both index benefit and avoided per-query eligibility work.

Recall measures agreement with exact embedding neighbors, not semantic relevance. No text inference, browser/API response, thumbnails, concurrency test, or controlled cold-disk test is included. Normal background service activity continued. The native extension is not integrated with the Go driver. See README.md for methodology and reproduction.
