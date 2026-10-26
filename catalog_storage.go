package main

import (
	"bytes"
	"compress/zlib"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"unicode"

	"modernc.org/sqlite"
)

const catalogVersion = 1

var metadataMagic = []byte{'H', 'I', 'M', 1}

func metadataJSON(value any) (string, error) {
	var raw []byte
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		raw = v
	default:
		return "", fmt.Errorf("invalid metadata type %T", value)
	}
	if !bytes.HasPrefix(raw, metadataMagic) {
		return string(raw), nil
	}
	r, err := zlib.NewReader(bytes.NewReader(raw[len(metadataMagic):]))
	if err != nil {
		return "", err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	return string(b), err
}
func packMetadata(raw string) ([]byte, error) {
	var b bytes.Buffer
	b.Write(metadataMagic)
	w := zlib.NewWriter(&b)
	if _, err := w.Write([]byte(raw)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Index human-readable string values, never JSON keys, numeric arrays, URLs or
// binary camera payloads. Keep all original values in the compressed metadata.
func metadataSearch(raw string) (string, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			x = strings.TrimSpace(strings.Trim(x, "\x00"))
			if x == "" {
				return
			}
			for _, r := range x {
				if r == unicode.ReplacementChar || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
					return
				}
			}
			seen[strings.ToLower(x)] = true
		case map[string]any:
			for k, v := range x {
				if k != "image_url" && k != "original_url" {
					walk(v)
				}
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(value)
	parts := make([]string, 0, len(seen))
	for s := range seen {
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n"), nil
}
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("metadata_json", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) { return metadataJSON(a[0]) })
	sqlite.MustRegisterDeterministicScalarFunction("metadata_pack", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		s, e := metadataJSON(a[0])
		if e != nil {
			return nil, e
		}
		return packMetadata(s)
	})
	sqlite.MustRegisterDeterministicScalarFunction("metadata_search", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		s, e := metadataJSON(a[0])
		if e != nil {
			return nil, e
		}
		return metadataSearch(s)
	})
}

// Version 0 is the original unversioned JSON catalog. Schema and data changes
// commit together; a failed/interrupted migration leaves version 0 intact.
func migrateCatalog(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > catalogVersion {
		return fmt.Errorf("catalog version %d is newer than supported version %d", version, catalogVersion)
	}
	if version == catalogVersion {
		return nil
	}
	log.Printf("Migrating catalog version %d to %d; compressing metadata and rebuilding search text", version, catalogVersion)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`ALTER TABLE images ADD COLUMN taken_at TEXT;
ALTER TABLE images ADD COLUMN created_at TEXT;
ALTER TABLE images ADD COLUMN size_bytes INTEGER;
UPDATE images SET taken_at=json_extract(metadata,'$.taken_at'),created_at=json_extract(metadata,'$.created_at'),size_bytes=json_extract(metadata,'$.size_bytes'),search=metadata_search(metadata),metadata=metadata_pack(metadata);
CREATE TRIGGER image_fields_insert AFTER INSERT ON images BEGIN
 UPDATE images SET taken_at=json_extract(metadata_json(new.metadata),'$.taken_at'),created_at=json_extract(metadata_json(new.metadata),'$.created_at'),size_bytes=json_extract(metadata_json(new.metadata),'$.size_bytes') WHERE id=new.id;
END;
CREATE TRIGGER image_fields_update AFTER UPDATE OF metadata ON images BEGIN
 UPDATE images SET taken_at=json_extract(metadata_json(new.metadata),'$.taken_at'),created_at=json_extract(metadata_json(new.metadata),'$.created_at'),size_bytes=json_extract(metadata_json(new.metadata),'$.size_bytes') WHERE id=new.id;
END;
PRAGMA user_version=1;`)
	if err != nil {
		return fmt.Errorf("migrate catalog: %w", err)
	}
	return tx.Commit()
}
