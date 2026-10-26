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
	registered, _ := sql.Open("sqlite", path)
	d := registered.Driver()
	_ = registered.Close()
	setup := func(conn sqlite.ExecQuerierContext) error {
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
	}
	db := sql.OpenDB(&sqliteConnector{driver: d, path: path, setup: setup})
	db.SetMaxOpenConns(1)
	return db
}

type sqliteConnector struct {
	driver driver.Driver
	path   string
	setup  func(sqlite.ExecQuerierContext) error
}

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	if err = c.setup(conn.(sqlite.ExecQuerierContext)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
func (c *sqliteConnector) Driver() driver.Driver { return c.driver }
