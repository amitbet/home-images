package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPhotoSortUsesIndexesInBothDirections(t *testing.T) {
	l, _, _ := fixture(t)
	for _, key := range []string{"taken_at", "created_at", "size"} {
		for _, direction := range []string{"asc", "desc"} {
			r := httptest.NewRequest("GET", "/api/search?sort="+key+"&direction="+direction, nil)
			order, args, err := l.ai.photoOrder(r)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := l.ai.read.Query("EXPLAIN QUERY PLAN SELECT i.id FROM catalog.images i ORDER BY "+order+" LIMIT 60", args...)
			if err != nil {
				t.Fatal(err)
			}
			usesIndex := false
			for rows.Next() {
				var a, b, c int
				var detail string
				if err = rows.Scan(&a, &b, &c, &detail); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(detail, "TEMP B-TREE") {
					t.Error(key, direction, detail)
				}
				usesIndex = usesIndex || strings.Contains(detail, "photo_sort_")
			}
			err = rows.Err()
			rows.Close()
			if err != nil || !usesIndex {
				t.Fatal(key, direction, err, usesIndex)
			}
		}
	}
}
