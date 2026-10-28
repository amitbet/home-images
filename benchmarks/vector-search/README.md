# SQLite vector search benchmark

This opt-in experiment compares the service's actual `AI.rankSelection` function
with Vectorlite HNSW on a fixed snapshot of the existing CLIP embeddings. It does
not modify or deploy the service, its databases, or its photos.

## Reproduce on the Windows image server

From the repository root, with Python 3.14 and Go 1.26.3:

```powershell
python -m venv benchmarks/vector-search/.venv
benchmarks/vector-search/.venv/Scripts/python.exe -m pip install -r benchmarks/vector-search/requirements.txt
benchmarks/vector-search/.venv/Scripts/python.exe -u benchmarks/vector-search/prepare.py
$env:HOME_IMAGES_VECTOR_BENCH = (Resolve-Path benchmarks/vector-search/data).Path
go test -run '^TestVectorIndexBenchmark$' -count=1 -v -timeout 15m
benchmarks/vector-search/.venv/Scripts/python.exe -u benchmarks/vector-search/benchmark.py
benchmarks/vector-search/.venv/Scripts/python.exe benchmarks/vector-search/validate.py
benchmarks/vector-search/.venv/Scripts/python.exe benchmarks/vector-search/report.py
```

The live source paths and configuration are specific to this installation.
Preparation uses SQLite's online backup API on read-only source connections,
then joins the two independently taken snapshots to define one fixed eligible
set. New indexing after that point is deliberately excluded. Existing queries
and snapshot artifacts live in ignored `data/`; they contain personal IDs and
embeddings and should not be committed. `results.json` contains only aggregates.

## Method

- CPU: Intel N150, 4 cores, approximately 16 GiB RAM, Windows amd64.
- 58,778 eligible normalized float32 CLIP vectors, 512 dimensions, from
  `ViT-B-32__openai` (model identity includes face model `buffalo_l`).
- Twenty different English text queries, embedded once by the actual Mac ML
  worker. The query list is in `prepare.py`. These are text-to-image queries;
  no indexed photo is used as its own query.
- Exact baseline: call the existing Go function with a populated text cache.
  It uses the existing modernc.org/sqlite driver, catalog/signature/model joins,
  blob decoding, scalar dot products, and top-k heap. One warmup per case, then
  twenty timed queries. The unmodified function supplies the reference IDs.
- HNSW: PyPI `vectorlite-py==0.2.0` native Windows DLL through Python's SQLite,
  cosine metric, M=16, ef_construction=200, random_seed=100. Search ef values:
  100, 500, 1000, 2000 (effective ef is at least k).
- Additional exact control: native SQLite calls Vectorlite's SIMD cosine-distance
  scalar function while scanning the same AI/catalog joins, ordered by distance
  with LIMIT k. One warmup and twenty timed queries per case. This separates
  the benefit of native execution/vector arithmetic from approximate indexing.
- Each HNSW setting/case runs the same twenty queries, one warmup and five timed
  repetitions per query (100 timing samples). Timings include SQL execution,
  graph search, image ID lookup, and result fetching. Repeat queries are warm
  searches; the Go baseline is also warmed but has fewer timing samples.
- Recall@k is the fraction of the exact Go top-k IDs returned, averaged over
  twenty queries; worst-query recall is also retained. This measures agreement
  with exact embedding search, not whether photos are semantically correct.
- Cases: entire eligible library at k=60 and k=500; largest exact directory at
  k=60 (3,124 candidates); largest person group at k=60 (5,091 candidates).
  These correspond to `hidden=all`; curation exclusions are not benchmarked.
- Filtered HNSW queries use precomputed candidate rowids with predicate pushdown.
  The metadata SQL cost to derive those same candidates is measured separately
  five times. Add that cost when interpreting an uncached filtered search.
- Build wall time, incremental process working set, serialized index size, save
  time, and reload time with a warm OS cache are recorded. Working set delta is
  an approximate process-level measurement, not an allocator-level accounting.

## Limits

Both paths run sequentially on the same machine while the normal image service
and its background indexing remain active. There is no controlled cold-disk
test, concurrency/load test, browser/API/JSON/thumbnail measurement, or full
library extrapolation. Text embedding inference/network latency is excluded
from retrieval timings. A first uncached query still needs that inference.

This is an algorithm and SQLite-extension feasibility benchmark, not a Go
integration benchmark: the current pure Go SQLite driver cannot simply load
this native DLL. Deploying it would require a compatible SQLite binding or
another integration design. Vectorlite 0.2.0 also uses a legacy persistence API
(third virtual-table argument); the current upstream API differs. Pin the
package version to reproduce this experiment. A production index must remain
synchronized with changes and recheck current eligibility and filters.

The Go experiment skips automatically unless `HOME_IMAGES_VECTOR_BENCH` is set.
No production implementation is changed.

## Metadata query-plan follow-up

Run `benchmarks/vector-search/.venv/Scripts/python.exe -u benchmarks/vector-search/filter_plans.py`
to compare original metadata-only eligibility queries against existing covering
indexes, a simplified person join, refreshed planner statistics, and an additional
binary-collation directory covering index. Every alternative is checked against
the original set of photo IDs. It records three warm timing samples and query
plans in `filter-results.json` (native Python SQLite, not the Go driver).

This follow-up runs `ANALYZE` and creates `bench_directory_exact` **on the benchmark
snapshots only**. Run `prepare.py` again before reproducing the original baseline
to restore snapshot statistics/schema from the service. The live databases and
service are not changed.

## Verify the implemented Go optimizations

After preparing the original snapshots and running the baseline Go benchmark:

```powershell
$env:HOME_IMAGES_VECTOR_BENCH_OPTIMIZED = '1'
go test -run '^TestVectorIndexBenchmark$' -count=1 -v -timeout 15m
Remove-Item Env:HOME_IMAGES_VECTOR_BENCH_OPTIMIZED
benchmarks/vector-search/.venv/Scripts/python.exe benchmarks/vector-search/optimizations.py
```

This mode creates the production directory index and refreshes statistics on
the snapshots, and uses the production person predicate. It preserves the
original `go-results.json`, writing `data/go-results-optimized.json` separately.
