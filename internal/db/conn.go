package db

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/satiyap/banyan/internal/pycompat"
)

// result is the rows of one query, as SQLite returned them: nil, int64,
// float64, string or []byte in each cell.
type result struct {
	cols []string
	rows [][]any
}

// column is every row's value under name.
func (r result) column(name string) []any {
	out := make([]any, 0, len(r.rows))
	for i, col := range r.cols {
		if col == name {
			for _, row := range r.rows {
				out = append(out, row[i])
			}
			break
		}
	}
	return out
}

type written struct {
	rowcount  int64
	lastrowid int64
}

// conn is one connection, behaving as Python's sqlite3 does by default: reads
// run on their own, and the first INSERT, UPDATE or DELETE opens a transaction
// that lasts until commit or rollback. With manual set nothing is opened
// implicitly (Python's isolation_level=None).
//
// The register opens a connection for each piece of work and closes it after,
// so a pragma or an open transaction never outlives the call that made it.
// database/sql pools connections, so each conn has a pool of its own, capped
// at one connection, with that connection held for the conn's whole life:
// every statement, BEGIN to COMMIT, runs on the same SQLite handle.
type conn struct {
	ctx           context.Context
	pool          *sql.DB
	raw           *sql.Conn
	manual        bool
	inTransaction bool
}

// dsn is path as the driver wants it. The driver reads anything after a '?'
// as options, so a path holding one goes as a file: URI with it escaped.
func dsn(path string) string {
	if !strings.Contains(path, "?") {
		return path
	}
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

func openConn(path string, manual bool) (*conn, error) {
	pool, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	ctx := context.Background()
	raw, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &conn{ctx: ctx, pool: pool, raw: raw, manual: manual}, nil
}

func (c *conn) close() {
	c.raw.Close()
	c.pool.Close()
}

func bound(params []any) ([]any, error) {
	out := make([]any, len(params))
	for i, p := range params {
		v, err := bind(p, i+1)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// exec runs SQL that is not a row change: a script, a PRAGMA, DDL, VACUUM.
func (c *conn) exec(query string, params ...any) error {
	args, err := bound(params)
	if err != nil {
		return err
	}
	_, err = c.raw.ExecContext(c.ctx, query, args...)
	return err
}

func (c *conn) query(query string, params []any, limit int) (result, error) {
	var out result
	args, err := bound(params)
	if err != nil {
		return out, err
	}
	rows, err := c.raw.QueryContext(c.ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	if out.cols, err = rows.Columns(); err != nil {
		return out, err
	}
	for (limit < 0 || len(out.rows) < limit) && rows.Next() {
		cells := make([]any, len(out.cols))
		targets := make([]any, len(cells))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return out, err
		}
		out.rows = append(out.rows, cells)
	}
	return out, rows.Err()
}

func (c *conn) all(query string, params ...any) (result, error) {
	return c.query(query, params, -1)
}

// get is the first row, or nil when there is none.
func (c *conn) get(query string, params ...any) ([]any, error) {
	res, err := c.query(query, params, 1)
	if err != nil || len(res.rows) == 0 {
		return nil, err
	}
	return res.rows[0], nil
}

// run is an INSERT, UPDATE or DELETE.
func (c *conn) run(query string, params ...any) (written, error) {
	args, err := bound(params)
	if err != nil {
		return written{}, err
	}
	if !c.manual && !c.inTransaction {
		if _, err := c.raw.ExecContext(c.ctx, "BEGIN"); err != nil {
			return written{}, err
		}
		c.inTransaction = true
	}
	res, err := c.raw.ExecContext(c.ctx, query, args...)
	if err != nil {
		return written{}, err
	}
	var out written
	if out.rowcount, err = res.RowsAffected(); err != nil {
		return out, err
	}
	out.lastrowid, err = res.LastInsertId()
	return out, err
}

func (c *conn) commit() error {
	if !c.inTransaction {
		return nil
	}
	if _, err := c.raw.ExecContext(c.ctx, "COMMIT"); err != nil {
		return err
	}
	c.inTransaction = false
	return nil
}

// rollback undoes the open transaction. Its own failure is not reported: it
// follows the refusal the caller is about to return, which says more.
func (c *conn) rollback() {
	if !c.inTransaction {
		return
	}
	c.inTransaction = false
	c.raw.ExecContext(c.ctx, "ROLLBACK")
}

// -- rows as structs --------------------------------------------------------

func fieldFor(v reflect.Value, column string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			if found := fieldFor(v.Field(i), column); found.IsValid() {
				return found
			}
			continue
		}
		if f.Tag.Get("json") == column {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// scan fills a row struct from one result row, matching each column to the
// field whose json tag names it. The match is by name because a register
// that gained columns by ALTER holds them in a different order.
func scan[T any](cols []string, cells []any) (T, error) {
	var out T
	v := reflect.ValueOf(&out).Elem()
	for i, col := range cols {
		f := fieldFor(v, col)
		if !f.IsValid() {
			continue
		}
		cell := cells[i]
		if b, ok := cell.([]byte); ok {
			cell = string(b)
		}
		ok := false
		switch f.Kind() {
		case reflect.String:
			var s string
			if s, ok = cell.(string); ok {
				f.SetString(s)
			}
		case reflect.Int64:
			var n int64
			if n, ok = cell.(int64); ok {
				f.SetInt(n)
			}
		case reflect.Pointer:
			var n int64
			if ok = cell == nil; !ok {
				if n, ok = cell.(int64); ok {
					f.Set(reflect.ValueOf(&n))
				}
			}
		}
		if !ok {
			return out, pycompat.TypeError("column %s holds %s where %s was expected", col, kindOf(cell), f.Type())
		}
	}
	return out, nil
}

func kindOf(cell any) string {
	switch cell.(type) {
	case nil:
		return "NULL"
	case int64:
		return "an INTEGER"
	case float64:
		return "a REAL"
	case string:
		return "TEXT"
	}
	return fmt.Sprintf("%T", cell)
}

func scanAll[T any](res result) ([]T, error) {
	out := make([]T, 0, len(res.rows))
	for _, cells := range res.rows {
		row, err := scan[T](res.cols, cells)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// list is every row of a query, as structs.
func list[T any](c *conn, query string, params ...any) ([]T, error) {
	res, err := c.all(query, params...)
	if err != nil {
		return nil, err
	}
	return scanAll[T](res)
}

// one is the first row of a query as a struct, or nil when there is none.
func one[T any](c *conn, query string, params ...any) (*T, error) {
	res, err := c.query(query, params, 1)
	if err != nil || len(res.rows) == 0 {
		return nil, err
	}
	row, err := scan[T](res.cols, res.rows[0])
	if err != nil {
		return nil, err
	}
	return &row, nil
}
