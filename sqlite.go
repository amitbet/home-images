package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sort"

	"modernc.org/sqlite"
)

// ATTACH and query_only belong to a physical connection, not to sql.DB. The
// SQLite driver can discard an interrupted connection; every replacement must
// receive the same setup before a query can use it.
func openSQLite(path string, attachments map[string]string, readOnly bool) *sql.DB {
	d := &sqlite.Driver{}
	d.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=10000", nil); err != nil {
			return err
		}
		names := make([]string, 0, len(attachments))
		for name := range attachments {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// Aliases are application constants; paths are always bound parameters.
			if name != "catalog" && name != "curation" {
				return bad(500, "Invalid database alias")
			}
			if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS "+name, []driver.NamedValue{{Ordinal: 1, Value: attachments[name]}}); err != nil {
				return err
			}
		}
		if readOnly {
			_, err := conn.ExecContext(ctx, "PRAGMA query_only=ON", nil)
			return err
		}
		return nil
	})
	db := sql.OpenDB(&sqliteConnector{driver: d, path: path})
	db.SetMaxOpenConns(1)
	return db
}

type sqliteConnector struct {
	driver *sqlite.Driver
	path   string
}

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.path)
}
func (c *sqliteConnector) Driver() driver.Driver { return c.driver }
