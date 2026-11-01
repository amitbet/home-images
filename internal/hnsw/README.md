Derived from github.com/coder/hnsw v0.6.1 (CC0-1.0; see LICENSE).

This internal copy keeps the graph and stream serialization APIs. The upstream
SavedGraph wrapper depends on renameio v1, which does not compile on Windows.
Home Images handles atomic cache persistence itself. Other changes use standard
library maps/slices and portable cosine distance instead of vek/SIMD.
No CGO, native extensions, or platform-specific binaries are required.

Search-layer uses a best-first frontier and a bounded max-heap, retaining
max(k, efSearch) candidates. Neighbor pruning preserves incoming edges. These
fixes are covered by the application recall test against exhaustive ranking.
UnitCosineDistance is used for pre-normalized vectors to avoid recalculating
their norms during graph construction and lookup.
Construction uses diversified neighbor selection over the full construction
candidate pool, preserving links between dense face clusters.
