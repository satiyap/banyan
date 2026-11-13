// The register against its recorded answers.
//
// tests/golden/db.json holds scripted scenarios: every return value, every
// exception, and the final contents and schema of every file left behind.
// Here the same scenarios are replayed against Database with the same clock,
// and everything must come out identical.
package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/satiyap/banyan/internal/pycompat"
)

const golden = "../../tests/golden"

type recordedError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type op struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
	Kwargs json.RawMessage   `json:"kwargs"`
	Result json.RawMessage   `json:"result"`
	Error  *recordedError    `json:"error"`
}

type recordedDump struct {
	Schema      json.RawMessage `json:"schema"`
	UserVersion json.RawMessage `json:"user_version"`
	Tables      map[string]struct {
		Columns json.RawMessage `json:"columns"`
		Rows    json.RawMessage `json:"rows"`
	} `json:"tables"`
}

type scenario struct {
	Name       string `json:"name"`
	Note       string `json:"note"`
	SetupSQL   string `json:"setup_sql"`
	StartFrom  string `json:"start_from"`
	ClockBase  string `json:"clock_base"`
	ClockCalls int    `json:"clock_calls"`
	Ops        []op   `json:"ops"`
	Final      struct {
		Files []string                `json:"files"`
		Dumps map[string]recordedDump `json:"dumps"`
	} `json:"final"`
}

func loadScenarios(t *testing.T) []scenario {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(golden, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Scenarios
}

// ------------------------------------------------------ reading the recording

// generic is JSON as plain values, numbers kept as written.
func generic(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("bad JSON %q: %v", data, err)
	}
	return out
}

func toJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// arg is positional argument i, or the zero value when it was not passed.
func arg[T any](t *testing.T, o op, i int) T {
	t.Helper()
	var out T
	if i < len(o.Args) {
		if err := json.Unmarshal(o.Args[i], &out); err != nil {
			t.Fatalf("%s argument %d %s: %v", o.Method, i, o.Args[i], err)
		}
	}
	return out
}

// kwargs is the keyword arguments, as T.
func kwargs[T any](t *testing.T, o op) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(o.Kwargs, &out); err != nil {
		t.Fatalf("%s keyword arguments %s: %v", o.Method, o.Kwargs, err)
	}
	return out
}

// take removes a keyword argument that is not a column and returns it.
func take[T any](t *testing.T, fields *Fields, name string) T {
	t.Helper()
	var out T
	if v, ok := fields.Get(name); ok {
		if out, ok = v.(T); !ok {
			t.Fatalf("keyword argument %s is %T", name, v)
		}
		fields.Delete(name)
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type call func(t *testing.T, d *Database, o op) (any, error)

// calls is each method as the recording names it (snake_case, with keyword
// arguments), and how it is called here.
var calls = map[string]call{
	"add_cheque": func(t *testing.T, d *Database, o op) (any, error) {
		return d.AddCheque(kwargs[NewCheque](t, o))
	},
	"update_cheque": func(t *testing.T, d *Database, o op) (any, error) {
		k := kwargs[Fields](t, o)
		changes := ChequeChanges{Event: take[string](t, &k, "event"), Detail: take[string](t, &k, "detail"), Columns: k}
		return nil, d.UpdateCheque(arg[int64](t, o, 0), changes)
	},
	"settle": func(t *testing.T, d *Database, o op) (any, error) {
		var outcomes []Outcome
		for _, pair := range arg[[][2]json.RawMessage](t, o, 0) {
			var out Outcome
			if json.Unmarshal(pair[0], &out.ChequeID) != nil || json.Unmarshal(pair[1], &out.Outcome) != nil {
				t.Fatalf("settle pair %s", pair)
			}
			outcomes = append(outcomes, out)
		}
		return d.Settle(outcomes, arg[string](t, o, 1))
	},
	"void": func(t *testing.T, d *Database, o op) (any, error) {
		return d.Void(arg[int64](t, o, 0), arg[string](t, o, 1))
	},
	"cheque_no_used": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ChequeNoUsed(arg[string](t, o, 0))
	},
	"create_batch": func(t *testing.T, d *Database, o op) (any, error) {
		return d.CreateBatch(arg[[]BatchItem](t, o, 0), arg[string](t, o, 1), arg[string](t, o, 2))
	},
	"get_batch": func(t *testing.T, d *Database, o op) (any, error) {
		return d.GetBatch(arg[int64](t, o, 0))
	},
	"open_batch": func(t *testing.T, d *Database, o op) (any, error) {
		return d.OpenBatch()
	},
	"move_batch": func(t *testing.T, d *Database, o op) (any, error) {
		var from []string
		if bytes.HasPrefix(o.Args[1], []byte(`"`)) {
			from = []string{arg[string](t, o, 1)}
		} else {
			from = arg[[]string](t, o, 1)
		}
		return d.MoveBatch(arg[int64](t, o, 0), from, arg[string](t, o, 2), kwargs[BatchChanges](t, o))
	},
	"save_batch_items": func(t *testing.T, d *Database, o op) (any, error) {
		return nil, d.SaveBatchItems(arg[int64](t, o, 0), arg[[]BatchItem](t, o, 1), arg[string](t, o, 2))
	},
	"check_entity": func(t *testing.T, d *Database, o op) (any, error) {
		return nil, d.CheckEntity(arg[*int64](t, o, 0), arg[string](t, o, 1))
	},
	"list_entities": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ListEntities()
	},
	"get_entity": func(t *testing.T, d *Database, o op) (any, error) {
		return d.GetEntity(arg[int64](t, o, 0))
	},
	"create_entity": func(t *testing.T, d *Database, o op) (any, error) {
		return d.CreateEntity(arg[string](t, o, 0), kwargs[EntityFields](t, o))
	},
	"update_entity": func(t *testing.T, d *Database, o op) (any, error) {
		return d.UpdateEntity(arg[int64](t, o, 0), kwargs[EntityFields](t, o))
	},
	"move_cheques": func(t *testing.T, d *Database, o op) (any, error) {
		return d.MoveCheques(arg[[]int64](t, o, 0), arg[int64](t, o, 1))
	},
	"delete_entity": func(t *testing.T, d *Database, o op) (any, error) {
		return d.DeleteEntity(arg[int64](t, o, 0))
	},
	"add_event": func(t *testing.T, d *Database, o op) (any, error) {
		return nil, d.AddEvent(arg[string](t, o, 0), arg[string](t, o, 1), kwargs[EventOptions](t, o))
	},
	"events_about": func(t *testing.T, d *Database, o op) (any, error) {
		return d.EventsAbout(arg[string](t, o, 0))
	},
	"cheque_events": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ChequeEvents(arg[[]string](t, o, 0))
	},
	"list_books": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ListBooks()
	},
	"get_book": func(t *testing.T, d *Database, o op) (any, error) {
		return d.GetBook(arg[int64](t, o, 0))
	},
	"add_book": func(t *testing.T, d *Database, o op) (any, error) {
		return d.AddBook(kwargs[NewBook](t, o))
	},
	"update_book": func(t *testing.T, d *Database, o op) (any, error) {
		k := kwargs[Fields](t, o)
		changes := BookChanges{
			Kind: take[string](t, &k, "kind"), Detail: take[string](t, &k, "detail"),
			Front: take[bool](t, &k, "front"), Columns: k,
		}
		return nil, d.UpdateBook(arg[int64](t, o, 0), changes)
	},
	"list_blank_leaves": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ListBlankLeaves()
	},
	"add_blank_leaves": func(t *testing.T, d *Database, o op) (any, error) {
		return d.AddBlankLeaves(arg[[]string](t, o, 0), arg[string](t, o, 1))
	},
	"used_numbers": func(t *testing.T, d *Database, o op) (any, error) {
		used, err := d.UsedNumbers()
		return sorted(used), err
	},
	"get_cheque": func(t *testing.T, d *Database, o op) (any, error) {
		return d.GetCheque(arg[int64](t, o, 0))
	},
	"list_cheques": func(t *testing.T, d *Database, o op) (any, error) {
		return d.ListCheques(kwargs[ListChequesOptions](t, o))
	},
	"issued_cheques": func(t *testing.T, d *Database, o op) (any, error) {
		return d.IssuedCheques()
	},
	"events_for": func(t *testing.T, d *Database, o op) (any, error) {
		return d.EventsFor(arg[int64](t, o, 0))
	},
	"entity_meta": func(t *testing.T, d *Database, o op) (any, error) {
		return d.EntityMeta()
	},
	"pending_jobs": func(t *testing.T, d *Database, o op) (any, error) {
		return d.PendingJobs()
	},
	"next_cheque_no": func(t *testing.T, d *Database, o op) (any, error) {
		return d.NextChequeNo(arg[string](t, o, 0))
	},
}

// recordedName is the Python exception each error stands for.
var recordedName = map[string]string{
	"ValueError":       "ValueError",
	"LookupError":      "LookupError",
	"KeyError":         "KeyError",
	"IntegrityError":   "IntegrityError",
	"OperationalError": "OperationalError",
	"MigrationError":   "RuntimeError",
}

// -------------------------------------------------- what is left on the disk

func rawRows(t *testing.T, raw *sql.DB, query string) (cols []string, out []map[string]any) {
	t.Helper()
	rows, err := raw.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, err = rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out = []map[string]any{}
	for rows.Next() {
		cells := make([]any, len(cols))
		targets := make([]any, len(cols))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, col := range cols {
			switch v := cells[i].(type) {
			case float64:
				// A REAL is written as Python writes a float, so 5.0 is not taken for 5.
				text := strconv.FormatFloat(v, 'g', -1, 64)
				if v == math.Trunc(v) && !strings.ContainsAny(text, "e") {
					text += ".0"
				}
				row[col] = json.Number(text)
			case []byte:
				t.Fatalf("%s: column %s holds a BLOB", query, col)
			default:
				row[col] = v
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols, out
}

type dumped struct {
	schema      any
	userVersion any
	tables      []string
	columns     map[string]any
	rows        map[string]any
}

func dump(t *testing.T, path string) dumped {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	_, schema := rawRows(t, raw, "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY name")
	out := dumped{columns: map[string]any{}, rows: map[string]any{}}
	out.schema = generic(t, toJSON(t, schema))
	for _, r := range schema {
		if r["type"] == "table" {
			out.tables = append(out.tables, r["name"].(string))
		}
	}
	sort.Strings(out.tables)
	for _, name := range out.tables {
		_, info := rawRows(t, raw, `PRAGMA table_info("`+name+`")`)
		columns := []any{}
		for _, r := range info {
			columns = append(columns, r["name"])
		}
		out.columns[name] = generic(t, toJSON(t, columns))
		_, rows := rawRows(t, raw, `SELECT * FROM "`+name+`" ORDER BY rowid`)
		out.rows[name] = generic(t, toJSON(t, rows))
	}
	_, version := rawRows(t, raw, "PRAGMA user_version")
	out.userVersion = generic(t, toJSON(t, version[0]["user_version"]))
	return out
}

var sidecar = regexp.MustCompile(`-(wal|shm|journal)$`)

// filesIn is the folder's files, without SQLite's own working files.
func filesIn(t *testing.T, folder string) []string {
	t.Helper()
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{}
	for _, e := range entries {
		if !sidecar.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// steppingClock is the recording's clock: a fixed moment, one second later
// each time it is read.
func steppingClock(t *testing.T, base string) (now func() time.Time, readings func() int) {
	t.Helper()
	start, err := time.Parse("2006-01-02T15:04:05", base[:19])
	if err != nil {
		t.Fatal(err)
	}
	millis, err := strconv.Atoi(base[20:23])
	if err != nil {
		t.Fatal(err)
	}
	start = start.Add(time.Duration(millis) * time.Millisecond)
	n := 0
	return func() time.Time {
		n++
		return start.Add(time.Duration(n-1) * time.Second)
	}, func() int { return n }
}

type outcome struct {
	Result any            `json:"result,omitempty"`
	Error  *recordedError `json:"error,omitempty"`
}

func (o outcome) String() string {
	if o.Error != nil {
		return fmt.Sprintf("%s: %s", o.Error.Type, o.Error.Message)
	}
	data, _ := json.Marshal(o.Result)
	return string(data)
}

// replay runs a scenario in a folder of its own and returns that folder. It
// counts every op it compared.
func replay(t *testing.T, s scenario, compared *int) string {
	t.Helper()
	folder := t.TempDir()
	path := filepath.Join(folder, "register.db")
	if s.StartFrom != "" {
		copyFile(t, filepath.Join(golden, s.StartFrom), path)
	}
	if s.SetupSQL != "" {
		// As Python's sqlite3 opens it: foreign keys are not enforced unless asked for.
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(s.SetupSQL); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	now, readings := steppingClock(t, s.ClockBase)
	var d *Database
	for i, o := range s.Ops {
		var result any
		var err error
		switch {
		case o.Method == "__open__":
			d = nil
			if opened, openErr := Open(path, Options{Now: now}); openErr != nil {
				err = openErr
			} else {
				d = opened
			}
		case o.Method == "increment_no":
			result = IncrementNo(arg[string](t, o, 0))
		case d == nil:
			err = &pycompat.Error{Type: "NotOpen", Message: "the register did not open"}
		default:
			fn, ok := calls[o.Method]
			if !ok {
				t.Fatalf("#%d: the recording calls %s, which is not replayed", i, o.Method)
			}
			result, err = fn(t, d, o)
		}

		var got outcome
		if err != nil {
			var named *pycompat.Error
			if !errors.As(err, &named) {
				t.Fatalf("#%d %s: an error that is not a *pycompat.Error: %v", i, o.Method, err)
			}
			kind := named.Type
			if kind != "NotOpen" {
				var ok bool
				if kind, ok = recordedName[named.Type]; !ok {
					t.Fatalf("#%d %s: unexpected %s: %s", i, o.Method, named.Type, named.Message)
				}
			}
			got.Error = &recordedError{Type: kind, Message: named.Message}
		} else {
			got.Result = generic(t, toJSON(t, result))
		}

		var want outcome
		if o.Error != nil {
			want.Error = o.Error
		} else {
			if o.Result == nil {
				t.Fatalf("#%d %s: the recording holds neither a result nor an error", i, o.Method)
			}
			want.Result = generic(t, o.Result)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("#%d %s(%s, %s)\n got  %s\n want %s", i, o.Method, o.Args, o.Kwargs, got, want)
		}
		*compared++
	}
	if readings() != s.ClockCalls {
		t.Errorf("clock readings: got %d, want %d", readings(), s.ClockCalls)
	}
	return folder
}

func same2(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Errorf("%s\n got  %s\n want %s", what, g, w)
	}
}

// --------------------------------------------------------------- the tests

func TestReplayRecordedScenarios(t *testing.T) {
	scenarios := loadScenarios(t)
	if len(scenarios) < 15 {
		t.Fatalf("only %d scenarios to replay", len(scenarios))
	}
	ran, ops, compared, files := 0, 0, 0, 0
	for _, s := range scenarios {
		ops += len(s.Ops)
		ran++
		t.Run(s.Name, func(t *testing.T) {
			folder := replay(t, s, &compared)
			got := filesIn(t, folder)
			if !reflect.DeepEqual(got, s.Final.Files) {
				t.Fatalf("files left behind: got %v, want %v", got, s.Final.Files)
			}
			if len(s.Final.Dumps) != len(s.Final.Files) {
				t.Fatalf("the recording dumps %d of %d files", len(s.Final.Dumps), len(s.Final.Files))
			}
			for _, file := range s.Final.Files {
				want, ok := s.Final.Dumps[file]
				if !ok {
					t.Fatalf("the recording has no dump of %s", file)
				}
				actual := dump(t, filepath.Join(folder, file))
				same2(t, file+" schema", actual.schema, generic(t, want.Schema))
				same2(t, file+" user_version", actual.userVersion, generic(t, want.UserVersion))
				wantTables := make([]string, 0, len(want.Tables))
				for name := range want.Tables {
					wantTables = append(wantTables, name)
				}
				sort.Strings(wantTables)
				if !reflect.DeepEqual(actual.tables, wantTables) {
					t.Fatalf("%s tables: got %v, want %v", file, actual.tables, wantTables)
				}
				for name, table := range want.Tables {
					same2(t, file+" "+name+" columns", actual.columns[name], generic(t, table.Columns))
					same2(t, file+" "+name+" rows", actual.rows[name], generic(t, table.Rows))
				}
				files++
			}
		})
	}
	if ran != len(scenarios) || compared != ops {
		t.Errorf("replayed %d of %d scenarios and %d of %d ops", ran, len(scenarios), compared, ops)
	}
	t.Logf("replayed %d scenarios, %d ops; compared %d files", ran, compared, files)
}

func TestCoversEveryPublicMethod(t *testing.T) {
	camel := func(name string) string {
		out := ""
		for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' }) {
			out += strings.ToUpper(part[:1]) + part[1:]
		}
		return out
	}
	replayed := map[string]bool{}
	for _, s := range loadScenarios(t) {
		for _, o := range s.Ops {
			replayed[camel(o.Method)] = true
		}
	}
	called := map[string]bool{}
	for name := range calls {
		called[camel(name)] = true
	}
	typ := reflect.TypeOf(&Database{})
	methods := map[string]bool{}
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; name != "Path" {
			methods[name] = true
		}
	}
	if !reflect.DeepEqual(called, methods) {
		t.Errorf("calls %v\nmethods %v", sorted(called), sorted(methods))
	}
	for name := range methods {
		if !replayed[name] {
			t.Errorf("%s is never replayed", name)
		}
	}
	if !replayed["IncrementNo"] {
		t.Error("IncrementNo is never replayed")
	}
}

// -- one file, either desk ---------------------------------------------

func copyGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "register.db")
	copyFile(t, filepath.Join(golden, name), path)
	return path
}

func journalMode(t *testing.T, path string) any {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, rows := rawRows(t, raw, "PRAGMA journal_mode")
	return rows[0]["journal_mode"]
}

func must[T any](value T, err error) func(t *testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, kind string) {
	t.Helper()
	if !IsA(err, kind) {
		t.Errorf("got %v, want a %s", err, kind)
	}
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func kinds(events []EventRow) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestCreatesTheSchemaOfTheRecordedEmptyRegister(t *testing.T) {
	path := filepath.Join(t.TempDir(), "register.db")
	must(Open(path, Options{}))(t)
	ours := dump(t, path)
	theirs := dump(t, copyGolden(t, "register-empty.db"))
	eq(t, ours, theirs)
	for _, s := range loadScenarios(t) {
		if s.Name == "empty" {
			want := s.Final.Dumps["register.db"]
			same2(t, "schema", ours.schema, generic(t, want.Schema))
			same2(t, "user_version", ours.userVersion, generic(t, want.UserVersion))
			for name, table := range want.Tables {
				same2(t, name+" columns", ours.columns[name], generic(t, table.Columns))
				same2(t, name+" rows", ours.rows[name], generic(t, table.Rows))
			}
		}
	}
	eq(t, journalMode(t, path), any("wal"))
	eq(t, journalMode(t, copyGolden(t, "register-empty.db")), any("wal"))
	eq(t, filesIn(t, filepath.Dir(path)), []string{"register.db"})
}

func TestOpensAnEarlierDesksRegisterUnchangedAndWorksInIt(t *testing.T) {
	path := copyGolden(t, "register-sample.db")
	before := dump(t, path)
	d := must(Open(path, Options{}))(t)
	eq(t, dump(t, path), before)
	eq(t, filesIn(t, filepath.Dir(path)), []string{"register.db"})

	var numbers []string
	for _, c := range must(d.ListCheques(ListChequesOptions{}))(t) {
		numbers = append(numbers, c.ChequeNo)
	}
	eq(t, numbers, []string{"000604", "000605", "000603", "000602", "000601"})
	found := false
	for _, e := range must(d.ListEntities())(t) {
		found = found || e.EntityName == "Lift AMC" || (e.EntityName == "" && e.PayeeName == "Lift AMC")
	}
	eq(t, found, true)
	eq(t, len(must(d.OpenBatch())(t).Items), 2)
	eq(t, sorted(must(d.UsedNumbers())(t)), []string{"000601", "000602", "000603", "000604", "000605", "000606"})

	// Cheque 5 awaits its verdict; cheque 3 failed and was voided.
	eq(t, must(d.GetCheque(5))(t).Status, "queued")
	eq(t, must(d.Settle([]Outcome{{3, "printed"}, {5, "printed"}}, "looks right"))(t), []int64{5})
	eq(t, must(d.Settle([]Outcome{{5, "failed"}}, ""))(t), []int64{})
	eq(t, must(d.GetCheque(5))(t).Status, "printed")
	eq(t, must(d.GetCheque(3))(t).Status, "void")

	id := must(d.AddCheque(NewCheque{
		F("cheque_no", must(d.NextChequeNo(""))(t)),
		F("payee", "Greenleaf Gardening"),
		F("amount_paise", 1250000),
		F("amount_words", "Rupees Invented Amount Only"),
		F("cheque_date", "2031-05-01"),
		F("status", "queued"),
	}))(t)
	eq(t, id, int64(6))
	added := must(d.GetCheque(id))(t)
	eq(t, added.ChequeNo, "000606")
	eq(t, *added.EntityID, int64(1))
	eq(t, added.EntityName, "Garden upkeep")
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+00:00$`).MatchString(added.CreatedAt) {
		t.Errorf("created_at %q", added.CreatedAt)
	}
	eq(t, must(d.Void(id, "pulled from the tray"))(t), true)
	eq(t, must(d.Void(id, "again"))(t), false)
	eq(t, must(d.Settle([]Outcome{{id, "printed"}}, ""))(t), []int64{})
	eq(t, kinds(must(d.EventsFor(id))(t)), []string{"created", "void"})
	_, err := d.AddBlankLeaves([]string{"000606"}, "torn")
	wantErr(t, err, "ValueError")
	eq(t, dump(t, path).schema, before.schema)
}

// -- the rules of the register -------------------------------------------

func open(t *testing.T) *Database {
	t.Helper()
	return must(Open(filepath.Join(t.TempDir(), "register.db"), Options{}))(t)
}

func addCheque(d *Database, status string, extra ...Field) (int64, error) {
	cheque := NewCheque{
		F("cheque_no", ""),
		F("payee", "Greenleaf Gardening"),
		F("amount_paise", 1250000),
		F("amount_words", "Rupees Invented Amount Only"),
		F("cheque_date", "2031-04-05"),
		F("status", status),
	}
	for _, f := range extra {
		cheque.Set(f.Name, f.Value)
	}
	return d.AddCheque(cheque)
}

func TestGivesAVerdictOnlyToAChequeAwaitingOne(t *testing.T) {
	d := open(t)
	ids := map[string]int64{}
	var all []Outcome
	for _, status := range []string{"queued", "printed", "failed", "void", "logged"} {
		ids[status] = must(addCheque(d, status))(t)
		all = append(all, Outcome{ids[status], "printed"})
	}
	eq(t, must(d.Settle(all, ""))(t), []int64{ids["queued"]})
	for _, status := range []string{"failed", "void", "logged"} {
		eq(t, must(d.GetCheque(ids[status]))(t).Status, status)
	}
	eq(t, must(d.Settle(all, ""))(t), []int64{})
	eq(t, kinds(must(d.EventsFor(ids["failed"]))(t)), []string{"created"})
}

func TestLetsOnlyOneOfTwoDesksSettleTheSameCheque(t *testing.T) {
	d := open(t)
	other := must(Open(d.Path(), Options{}))(t)
	id := must(addCheque(d, "queued"))(t)
	eq(t, must(d.Settle([]Outcome{{id, "printed"}}, ""))(t), []int64{id})
	eq(t, must(other.Settle([]Outcome{{id, "failed"}}, ""))(t), []int64{})
	eq(t, must(other.Void(id, "stopped"))(t), true)
	eq(t, must(d.Void(id, "stopped twice"))(t), false)
	eq(t, must(d.GetCheque(id))(t).Status, "void")
}

func TestSettlesInTheOrderGiven(t *testing.T) {
	d := open(t)
	a, b, c := must(addCheque(d, "queued"))(t), must(addCheque(d, "queued"))(t), must(addCheque(d, "queued"))(t)
	eq(t, must(d.Settle([]Outcome{{c, "printed"}, {a, "failed"}}, ""))(t), []int64{c, a})
	eq(t, must(d.Settle([]Outcome{{b, "printed"}}, ""))(t), []int64{b})
}

func TestNeverDeletesAChequeAndRecordsEveryChange(t *testing.T) {
	d := open(t)
	id := must(addCheque(d, "queued"))(t)
	ok(t, d.UpdateCheque(id, ChequeChanges{Event: "queued", Detail: "job Desk_Laser-1", Columns: Fields{F("job_id", "Desk_Laser-1")}}))
	must(d.Settle([]Outcome{{id, "failed"}}, "feed jammed"))(t)
	must(d.Void(id, "leaf destroyed"))(t)
	cheques := must(d.ListCheques(ListChequesOptions{}))(t)
	eq(t, len(cheques), 1)
	eq(t, cheques[0].ID, id)
	eq(t, cheques[0].Status, "void")
	eq(t, kinds(must(d.EventsFor(id))(t)), []string{"created", "queued", "confirmed-failed", "void"})
	eq(t, must(d.DeleteEntity(*must(d.GetCheque(id))(t).EntityID))(t), false)
	typ := reflect.TypeOf(d)
	for i := 0; i < typ.NumMethod(); i++ {
		name := strings.ToLower(typ.Method(i).Name)
		if strings.Contains(name, "cheque") && (strings.Contains(name, "delete") || strings.Contains(name, "remove")) {
			t.Errorf("%s deletes a cheque", typ.Method(i).Name)
		}
	}
}

func TestRollsARefusedWriteBackWhole(t *testing.T) {
	d := open(t)
	_, err := addCheque(d, "nonsense", F("payee", "Nobody Plumbing"))
	wantErr(t, err, "IntegrityError")
	eq(t, must(d.ListEntities())(t), []EntityRow{})
	_, err = d.AddBlankLeaves([]string{"000901", "000901"}, "twice")
	wantErr(t, err, "IntegrityError")
	eq(t, must(d.ListBlankLeaves())(t), []BlankLeafRow{})
	eq(t, must(d.EventsAbout("leaf:000901"))(t), []EventRow{})
}

func TestReturnsNumberIdsAndMoneyAsWholePaise(t *testing.T) {
	d := open(t)
	id := must(addCheque(d, "logged", F("amount_paise", int64(123456789012))))(t)
	eq(t, must(d.GetCheque(id))(t).AmountPaise, int64(123456789012))
	if must(d.GetCheque(999))(t) != nil || must(d.GetEntity(999))(t) != nil || must(d.GetBook(999))(t) != nil ||
		must(d.GetBatch(999))(t) != nil || must(d.OpenBatch())(t) != nil {
		t.Error("a row that is not there is not nil")
	}
}

func TestStoresWholeNumbersAsIntegersAndBooleansAsOneAndZero(t *testing.T) {
	d := open(t)
	entity := must(d.CreateEntity("Meridian Lifts", EntityFields{F("ac_payee", false), F("seal_required", true), F("notes", 7.0)}))(t)
	row := must(d.GetEntity(entity))(t)
	eq(t, []any{row.AcPayee, row.SealRequired, row.Notes}, []any{int64(0), int64(1), "7"})
	id := must(addCheque(d, "logged", F("cheque_no", 41)))(t)
	eq(t, must(d.GetCheque(id))(t).ChequeNo, "41")
}

func TestStampsTimeAsUTCWholeSecondsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "register.db")
	zone := time.FixedZone("IST", 5*3600+1800)
	d := must(Open(path, Options{Now: func() time.Time {
		return time.Date(2031, 12, 31, 23, 59, 59, 999e6, time.UTC).In(zone)
	}}))(t)
	ok(t, d.AddEvent("note", "", EventOptions{}))
	eq(t, must(d.EventsAbout(""))(t)[0].At, "2031-12-31T23:59:59+00:00")
}

func TestNamesItsErrorsAsTheRecordingsDo(t *testing.T) {
	d := open(t)
	wantErr(t, KeyError("'payee'"), "LookupError")
	wantErr(t, IntegrityError("x"), "DatabaseError")
	wantErr(t, OperationalError("x"), "DatabaseError")
	if IsA(LookupError("x"), "KeyError") || IsA(errors.New("x"), "ValueError") {
		t.Error("IsA is too generous")
	}
	_, err := d.UpdateEntity(99, EntityFields{F("notes", "x")})
	wantErr(t, err, "LookupError")
	_, err = d.AddCheque(NewCheque{F("amount_paise", 1)})
	wantErr(t, err, "KeyError")
	wantErr(t, d.UpdateCheque(99, ChequeChanges{Event: "edited"}), "IntegrityError")
	wantErr(t, d.UpdateCheque(1, ChequeChanges{Columns: Fields{F("colour", "blue")}}), "OperationalError")
	must(d.CreateEntity("Meridian Lifts", nil))(t)
	_, err = d.CreateEntity("Meridian Lifts", nil)
	wantErr(t, err, "ValueError")
}

// -- the helpers the port adds --------------------------------------------

func TestFieldsKeepTheirOrderThroughJSON(t *testing.T) {
	const text = `{"z":1,"a":{"y":null,"b":[1.5,"é<",true]},"m":"x"}`
	var f Fields
	ok(t, json.Unmarshal([]byte(text), &f))
	eq(t, f.names(), []string{"z", "a", "m"})
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	ok(t, enc.Encode(f))
	eq(t, strings.TrimSpace(b.String()), text)
	eq(t, pyJSONDumps([]BatchItem{f}), `[{"z": 1, "a": {"y": null, "b": [1.5, "\u00e9<", true]}, "m": "x"}]`)
	eq(t, pyJSONDumps("😀\x7f"), `"\ud83d\ude00\u007f"`)
}

func TestWritesNumbersAsJavaScriptDoes(t *testing.T) {
	for v, want := range map[float64]string{
		0: "0", 5: "5", -2.5: "-2.5", 1e21: "1e+21", 1e20: "100000000000000000000",
		1.5e-7: "1.5e-7", 0.000001: "0.000001", 123456789012: "123456789012", 0.1: "0.1",
	} {
		eq(t, jsNumber(v), want)
	}
}

func TestReadsTheDriversErrorText(t *testing.T) {
	for text, want := range map[string]string{
		"constraint failed: UNIQUE constraint failed: blank_leaves.cheque_no (2067)": "UNIQUE constraint failed: blank_leaves.cheque_no",
		"SQL logic error: no such column: colour (1)":                                "no such column: colour",
		"database is locked (5) (SQLITE_BUSY)":                                       "database is locked",
		"constraint failed: CHECK constraint failed: status IN\n  ('a') (275)":       "CHECK constraint failed: status IN\n  ('a')",
	} {
		eq(t, sqliteMessage(text), want)
	}
}
