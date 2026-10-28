# Go search optimizations — 2026-10-27

All 80 query results have identical candidate counts, ordered photo IDs and scores.

| Search | Before median | After median | Speedup |
|---|---:|---:|---:|
| all_top60 | 1546.97 ms | 1496.01 ms | 1.0× |
| all_top500 | 1569.68 ms | 1497.86 ms | 1.0× |
| directory_top60 | 1912.61 ms | 123.23 ms | 15.5× |
| person_top60 | 1946.81 ms | 261.35 ms | 7.4× |

Same 58,778-vector snapshot and 20 text queries per case as the original experiment. Times include actual Go SQL joins, reading vectors, decoding, dot products, and ranking. Text inference and API response formatting are excluded. Normal background service work continued.

Changes: exact-directory covering index; person-first joins with covering indexes; planner statistics maintenance. No approximate vector index is used.
