package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/duckdb/duckdb-go/v2"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/logs"
	"github.com/tjorourke/flightbay/internal/policy"
)

// The workspace keeps log lines, and a queryable copy of objects and events,
// in DuckDB. Lines are appended one chunk per transaction, so a search sees
// whole chunks or nothing, and reads run while the import is still appending.
// Log text never has to fit in Go memory.
const schema = `
CREATE TABLE IF NOT EXISTS log_lines (
  id BIGINT, seq INTEGER, ts BIGINT,
  cluster VARCHAR, namespace VARCHAR, pod VARCHAR, container VARCHAR, stream VARCHAR, node VARCHAR,
  level VARCHAR, scope VARCHAR, msg VARCHAR, fields MAP(VARCHAR, VARCHAR), raw VARCHAR, chunk VARCHAR);
CREATE TABLE IF NOT EXISTS objects (
  cluster VARCHAR, api_group VARCHAR, version VARCHAR, resource VARCHAR, kind VARCHAR,
  namespace VARCHAR, name VARCHAR, labels MAP(VARCHAR, VARCHAR), object VARCHAR);
CREATE TABLE IF NOT EXISTS events (
  cluster VARCHAR, namespace VARCHAR, type VARCHAR, reason VARCHAR, object_kind VARCHAR, object_name VARCHAR,
  message VARCHAR, count BIGINT, last_seen VARCHAR, event VARCHAR);
CREATE OR REPLACE VIEW logs AS
  SELECT id, make_timestamp(ts // 1000) AS time, cluster, namespace, pod, container, stream, node, level, scope, msg, fields, raw
  FROM log_lines;`

const lineCols = "id, seq, ts, cluster, namespace, pod, container, stream, node, level, scope, msg, fields, raw, chunk"

type logDB struct {
	db      *sql.DB
	path    string
	jobs    chan func()
	pending sync.WaitGroup
}

func openDB(path string) (*logDB, error) {
	if path != "" {
		_ = os.Remove(path)
		_ = os.Remove(path + ".wal")
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Queries typed by engineers or agents run against this database. Turn off
	// its access to the local file system and lock that setting.
	for _, s := range []string{"SET enable_external_access = false", "SET lock_configuration = true"} {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	l := &logDB{db: db, path: path, jobs: make(chan func(), 16)}
	// Appending pays a cgo call per value, so chunks are parsed and appended
	// by a few workers, each on its own connection. Chunks are independent:
	// ids and sequence numbers are reserved in bundle order before queueing.
	for i := 0; i < max(2, min(16, runtime.GOMAXPROCS(0))); i++ {
		go func() {
			for j := range l.jobs {
				j()
			}
		}()
	}
	return l, nil
}

func (l *logDB) appendRows(table string, rows [][]driver.Value) error {
	if len(rows) == 0 {
		return nil
	}
	conn, err := l.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		a, err := duckdb.NewAppenderFromConn(dc.(driver.Conn), "", table)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := a.AppendRow(r...); err != nil {
				_ = a.Close()
				return err
			}
		}
		return a.Close() // commits the chunk
	})
}

func toMap(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}
	out := duckdb.Map{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func str(v any) string { s, _ := v.(string); return s }

// Close waits for queued log chunks and closes the database.
func (w *Workspace) Close() error {
	if w.ldb == nil {
		return nil
	}
	w.ldb.pending.Wait()
	return w.ldb.db.Close()
}

// WaitLogs blocks until every queued log chunk is committed.
func (w *Workspace) WaitLogs() { w.ldb.pending.Wait() }

// DBPath is the DuckDB file behind the workspace ("" when in memory).
func (w *Workspace) DBPath() string {
	if w.ldb == nil {
		return ""
	}
	return w.ldb.path
}

func streamKey(cluster, ns, pod, container, stream string) string {
	return cluster + "/" + ns + "/" + pod + "/" + container + "/" + stream
}

// AddLogLines parses and commits one log chunk before returning.
func (w *Workspace) AddLogLines(cluster, ns, pod, container, stream string, lines []string, chunk string) {
	done := make(chan struct{})
	w.QueueLogLines(cluster, ns, pod, container, stream, lines, chunk, func() { close(done) })
	<-done
}

// QueueLogLines reserves ids for a chunk in order and commits it in the
// background; searches see it once committed. Call WaitLogs before treating
// the import as complete.
func (w *Workspace) QueueLogLines(cluster, ns, pod, container, stream string, lines []string, chunk string, done func()) {
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		if done != nil {
			done()
		}
		return
	}
	w.mu.Lock()
	node := ""
	if o := w.objects[objKey(cluster, "", "pods", ns, pod)]; o != nil {
		spec, _ := o.Obj["spec"].(map[string]any)
		node, _ = spec["nodeName"].(string)
	}
	key := streamKey(cluster, ns, pod, container, stream)
	seq := w.streamSeq[key]
	firstID := w.nextID + 1
	w.nextID += int64(len(kept))
	w.streamSeq[key] = seq + len(kept)
	w.mu.Unlock()
	w.ldb.pending.Add(1)
	w.ldb.jobs <- func() {
		defer w.ldb.pending.Done()
		if done != nil {
			defer done()
		}
		w.appendLines(firstID, seq, cluster, ns, pod, container, stream, node, chunk, kept)
	}
}

func (w *Workspace) appendLines(firstID int64, seq int, cluster, ns, pod, container, stream, node, chunk string, kept []string) {
	rows := make([][]driver.Value, 0, len(kept))
	var start, end time.Time
	for i, l := range kept {
		ts, rest := logs.SplitTimestamp(l)
		p := logs.Parse(rest)
		var tsn int64
		if !ts.IsZero() {
			tsn = ts.UnixNano()
			if ts.After(end) {
				end = ts
			}
			if start.IsZero() || ts.Before(start) {
				start = ts
			}
		}
		rows = append(rows, []driver.Value{firstID + int64(i), int32(seq + i), tsn, cluster, ns, pod, container, stream, node,
			p.Level, p.Scope, p.Msg, toMap(p.Fields), rest, chunk})
	}
	if err := w.ldb.appendRows("log_lines", rows); err != nil {
		w.SetState(func(s *ImportState) { s.Error = "log store: " + err.Error() })
		return
	}
	w.mu.Lock()
	w.state.LogLines += len(rows)
	if end.After(w.logEnd) {
		w.logEnd = end
	}
	if !start.IsZero() && (w.logStart.IsZero() || start.Before(w.logStart)) {
		w.logStart = start
	}
	w.commit()
	w.mu.Unlock()
}

func (w *Workspace) mirrorObjects(cluster string, r bundle.APIResource, items []map[string]any) {
	rows := make([][]driver.Value, 0, len(items))
	for _, it := range items {
		md, _ := it["metadata"].(map[string]any)
		lbl := map[string]string{}
		if l, ok := md["labels"].(map[string]any); ok {
			for k, v := range l {
				lbl[k] = str(v)
			}
		}
		b, _ := json.Marshal(it)
		rows = append(rows, []driver.Value{cluster, r.Group, r.Version, r.Resource, r.Kind, str(md["namespace"]), str(md["name"]), toMap(lbl), string(b)})
	}
	_ = w.ldb.appendRows("objects", rows)
}

func (w *Workspace) mirrorEvents(cluster string, items []map[string]any) {
	rows := make([][]driver.Value, 0, len(items))
	for _, it := range items {
		md, _ := it["metadata"].(map[string]any)
		io, _ := it["involvedObject"].(map[string]any)
		if io == nil {
			io, _ = it["regarding"].(map[string]any)
		}
		last := str(it["lastTimestamp"])
		if last == "" {
			last = str(it["eventTime"])
		}
		var count int64
		if c, ok := it["count"].(float64); ok {
			count = int64(c)
		}
		msg := str(it["message"])
		if msg == "" {
			msg = str(it["note"])
		}
		b, _ := json.Marshal(it)
		rows = append(rows, []driver.Value{cluster, str(md["namespace"]), str(it["type"]), str(it["reason"]), str(io["kind"]), str(io["name"]), msg, count, last, string(b)})
	}
	_ = w.ldb.appendRows("events", rows)
}

func scanLines(rows *sql.Rows) ([]*logs.Record, error) {
	defer rows.Close()
	var out []*logs.Record
	for rows.Next() {
		var (
			id, ts                                                                   int64
			seq                                                                      int32
			cluster, ns, pod, container, stream, node, level, scope, msg, raw, chunk sql.NullString
			fields                                                                   any
		)
		if err := rows.Scan(&id, &seq, &ts, &cluster, &ns, &pod, &container, &stream, &node, &level, &scope, &msg, &fields, &raw, &chunk); err != nil {
			return nil, err
		}
		r := &logs.Record{ID: id, Seq: int(seq), Source: &logs.Source{Cluster: cluster.String, Namespace: ns.String, Pod: pod.String, Container: container.String,
			Stream: stream.String, Node: node.String, Chunk: chunk.String}, Level: level.String, Scope: scope.String, Msg: msg.String, Raw: raw.String}
		if ts != 0 {
			r.TS = time.Unix(0, ts).UTC()
		}
		if m := mapValue(fields); len(m) > 0 {
			sm := make(map[string]string, len(m))
			for k, v := range m {
				sm[k] = fmt.Sprint(v)
			}
			r.Fields = logs.NewFields(sm)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LogStream returns a captured container stream in line order.
func (w *Workspace) LogStream(cluster, ns, pod, container, stream string) []*logs.Record {
	return w.LogStreamFrom(cluster, ns, pod, container, stream, 0)
}

// LogStreamFrom returns a stream's lines from sequence number from onwards.
func (w *Workspace) LogStreamFrom(cluster, ns, pod, container, stream string, from int) []*logs.Record {
	rows, err := w.ldb.db.Query("SELECT "+lineCols+" FROM log_lines WHERE cluster = ? AND namespace = ? AND pod = ? AND container = ? AND stream = ? AND seq >= ? ORDER BY seq",
		cluster, ns, pod, container, stream, from)
	if err != nil {
		return nil
	}
	out, _ := scanLines(rows)
	return out
}

// HasStream reports whether any line of a stream was loaded.
func (w *Workspace) HasStream(cluster, ns, pod, container, stream string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.streamSeq[streamKey(cluster, ns, pod, container, stream)] > 0
}

// Streams lists the captured streams for a pod as container/stream.
func (w *Workspace) Streams(cluster, ns, pod string) []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	prefix := cluster + "/" + ns + "/" + pod + "/"
	var out []string
	for k := range w.streamSeq {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// LogFilter selects log records.
type LogFilter struct {
	Query     string
	Cluster   string
	Namespace string
	Pod       string
	Container string
	Level     string
	Stream    string
	From, To  time.Time
	Limit     int
	Offset    int
}

// Bucket is one histogram bar.
type Bucket struct {
	Start time.Time `json:"start"`
	Count int       `json:"count"`
}

// LogResult is a consistent page of search results.
type LogResult struct {
	Total     int                       `json:"total"`
	Records   []*logs.Record            `json:"records"`
	Facets    map[string]map[string]int `json:"facets"`
	Histogram []Bucket                  `json:"histogram"`
	Revision  int64                     `json:"revision"`
	Scanned   int                       `json:"scanned"`
	Loading   bool                      `json:"loading"`
	Message   string                    `json:"message,omitempty"`
	From      time.Time                 `json:"from"`
	To        time.Time                 `json:"to"`
}

var facetCols = []string{"level", "namespace", "pod", "container", "stream", "cluster"}

// SearchLogs evaluates a query over every committed line. The matches go into
// a temporary table once; facets, the histogram, the count and the page are
// all computed from it, so the text search runs once per request.
func (w *Workspace) SearchLogs(f LogFilter) (*LogResult, error) {
	q, err := logs.ParseQuery(f.Query)
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	ref, rev, scanned := w.logEnd, w.rev, w.state.LogLines
	loading := w.state.Phase == PhaseLoading || w.state.Phase == PhaseWaiting
	pol := w.policy
	w.mu.RUnlock()
	if f.Limit <= 0 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	res := &LogResult{Facets: map[string]map[string]int{}, Revision: rev, Scanned: scanned, Loading: loading}
	for _, c := range facetCols {
		res.Facets[c] = map[string]int{}
	}
	where, args := q.SQL(ref)
	if f.Cluster != "" {
		where += " AND cluster = ?"
		args = append(args, f.Cluster)
	}
	ctx := context.Background()
	// 1. Facets over the query alone: they ignore the facet filters so users
	// can widen a selection. One pass with grouping sets.
	rows, err := w.ldb.db.QueryContext(ctx, `SELECT GROUPING(level), GROUPING(namespace), GROUPING(pod), GROUPING(container), GROUPING(stream),
	  coalesce(level, ''), coalesce(namespace, ''), coalesce(pod, ''), coalesce(container, ''), coalesce(stream, ''), coalesce(cluster, ''), count(*)
	  FROM log_lines WHERE `+where+` GROUP BY GROUPING SETS ((level), (namespace), (pod), (container), (stream), (cluster))`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g [5]int
		var v [6]string
		var n int
		if err := rows.Scan(&g[0], &g[1], &g[2], &g[3], &g[4], &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &n); err != nil {
			rows.Close()
			return nil, err
		}
		col := 5 // cluster: every other column rolled up
		for k := 0; k < 5; k++ {
			if g[k] == 0 {
				col = k
			}
		}
		res.Facets[facetCols[col]][v[col]] += n
	}
	rows.Close()

	fw, fargs := where, append([]any{}, args...)
	for _, fc := range []struct{ col, v string }{{"namespace", f.Namespace}, {"pod", f.Pod}, {"container", f.Container}, {"level", f.Level}, {"stream", f.Stream}} {
		if fc.v != "" {
			fw += " AND " + fc.col + " = ?"
			fargs = append(fargs, fc.v)
		}
	}
	rw, rargs := fw, append([]any{}, fargs...)
	if !f.From.IsZero() {
		rw += " AND ts >= ?"
		rargs = append(rargs, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		rw += " AND ts <= ?"
		rargs = append(rargs, f.To.UnixNano())
	}
	// 2. Histogram over the capture's time range, and the total, in one pass.
	w.mu.RLock()
	lo, hi := w.logStart, w.logEnd
	w.mu.RUnlock()
	// Zoomed in: the histogram's axis is the selected range, so each drag
	// re-buckets into finer bars.
	if !f.From.IsZero() {
		lo = f.From
	}
	if !f.To.IsZero() {
		hi = f.To
	}
	const nb = 48
	span := hi.Sub(lo)
	if span < time.Minute {
		span = time.Minute
	}
	step := span / nb
	inRange := "TRUE"
	var hargs []any
	if !f.From.IsZero() {
		inRange += " AND ts >= ?"
		hargs = append(hargs, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		inRange += " AND ts <= ?"
		hargs = append(hargs, f.To.UnixNano())
	}
	hrows, err := w.ldb.db.QueryContext(ctx, fmt.Sprintf(`SELECT CASE WHEN ts = 0 THEN -1 ELSE greatest(0, least((ts - %d) // %d, %d)) END AS b,
	  count(*), count(*) FILTER (WHERE %s) FROM log_lines WHERE %s GROUP BY b`, lo.UnixNano(), int64(step), nb-1, inRange, fw), append(hargs, fargs...)...)
	if err != nil {
		return nil, err
	}
	if !lo.IsZero() {
		res.Histogram = make([]Bucket, nb)
		for i := range res.Histogram {
			res.Histogram[i].Start = lo.Add(time.Duration(i) * step)
		}
		res.From, res.To = lo, hi
	}
	for hrows.Next() {
		var b int64
		var all, inr int
		if err := hrows.Scan(&b, &all, &inr); err != nil {
			hrows.Close()
			return nil, err
		}
		res.Total += inr
		if b >= 0 && int(b) < len(res.Histogram) {
			res.Histogram[b].Count += inr // only lines inside the range being shown
		}
		_ = all
	}
	hrows.Close()
	// 3. The page, newest first.
	page, err := w.ldb.db.QueryContext(ctx, "SELECT "+lineCols+" FROM log_lines WHERE "+rw+" ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?", append(rargs, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	if res.Records, err = scanLines(page); err != nil {
		return nil, err
	}
	switch {
	case res.Total > 0:
	case pol != nil && !pol.Plan().Selected(policy.CatLogs):
		res.Message = "Logs were excluded by the customer's collection policy."
	case res.Loading:
		res.Message = "No matches in the data loaded so far."
	default:
		res.Message = "No matches in the captured logs."
	}
	return res, nil
}

// FieldValues is one parsed field with its most common values.
type FieldValues struct {
	Field  string         `json:"field"`
	Count  int            `json:"count"`
	Values []FieldCounter `json:"values"`
}

// FieldCounter is one value and how many matching lines carry it.
type FieldCounter struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// LogFields lists the parsed fields present in lines matching the query,
// most common first, each with its top values.
func (w *Workspace) LogFields(query, cluster string, maxFields, maxValues int) ([]FieldValues, error) {
	q, err := logs.ParseQuery(query)
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	ref := w.logEnd
	w.mu.RUnlock()
	where, args := q.SQL(ref)
	if cluster != "" {
		where += " AND cluster = ?"
		args = append(args, cluster)
	}
	rows, err := w.ldb.db.Query(`WITH kv AS (SELECT unnest(map_keys(fields)) AS k, unnest(map_values(fields)) AS v FROM log_lines WHERE fields IS NOT NULL AND `+where+`),
	  keys AS (SELECT k, count(*) AS n FROM kv GROUP BY k ORDER BY n DESC LIMIT ?),
	  vals AS (SELECT k, v, count(*) AS n, row_number() OVER (PARTITION BY k ORDER BY count(*) DESC, v) AS r FROM kv WHERE k IN (SELECT k FROM keys) GROUP BY k, v)
	  SELECT keys.k, keys.n, vals.v, vals.n FROM keys JOIN vals USING (k) WHERE vals.r <= ? ORDER BY keys.n DESC, keys.k, vals.n DESC`, append(args, maxFields, maxValues)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FieldValues{}
	for rows.Next() {
		var k, v string
		var kn, vn int
		if err := rows.Scan(&k, &kn, &v, &vn); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Field != k {
			out = append(out, FieldValues{Field: k, Count: kn})
		}
		if len(v) > 120 {
			v = v[:120]
		}
		out[len(out)-1].Values = append(out[len(out)-1].Values, FieldCounter{Value: v, Count: vn})
	}
	return out, rows.Err()
}

// LogContext returns up to n lines either side of a line in its stream.
func (w *Workspace) LogContext(id int64, n int) []*logs.Record {
	rows, err := w.ldb.db.Query(`SELECT `+lineCols+` FROM log_lines l WHERE (cluster, namespace, pod, container, stream) =
	  (SELECT (cluster, namespace, pod, container, stream) FROM log_lines WHERE id = ?)
	  AND seq BETWEEN (SELECT seq FROM log_lines WHERE id = ?) - ? AND (SELECT seq FROM log_lines WHERE id = ?) + ? ORDER BY seq`, id, id, n, id, n)
	if err != nil {
		return nil
	}
	out, _ := scanLines(rows)
	return out
}

// FindLogs returns lines matching a query in load order, at most limit.
func (w *Workspace) FindLogs(query string, limit int) ([]*logs.Record, error) {
	q, err := logs.ParseQuery(query)
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	ref := w.logEnd
	w.mu.RUnlock()
	where, args := q.SQL(ref)
	rows, err := w.ldb.db.Query("SELECT "+lineCols+" FROM log_lines WHERE "+where+" ORDER BY id LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return scanLines(rows)
}

// ErrorMinute counts error lines in one namespace in one minute.
type ErrorMinute struct {
	Cluster, Namespace string
	Minute             time.Time
	Count              int
}

// ErrorMinutes returns minutes with at least min error lines per namespace.
func (w *Workspace) ErrorMinutes(cluster string, min int) []ErrorMinute {
	rows, err := w.ldb.db.Query(`SELECT cluster, namespace, ts // 60000000000 AS minute, count(*) FROM log_lines
	  WHERE level = 'error' AND ts <> 0 AND (? = '' OR cluster = ?) GROUP BY ALL HAVING count(*) >= ?`, cluster, cluster, min)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ErrorMinute
	for rows.Next() {
		var e ErrorMinute
		var m int64
		if rows.Scan(&e.Cluster, &e.Namespace, &m, &e.Count) == nil {
			e.Minute = time.Unix(m*60, 0).UTC()
			out = append(out, e)
		}
	}
	return out
}

// SQLResult is the result of a read-only query.
type SQLResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
	Millis    int64    `json:"ms"`
}

var sqlStart = regexp.MustCompile(`(?is)^\s*(select|with|from|describe|show|summarize|explain|values|table|pivot|unpivot)\b`)

// ErrNotReadOnly rejects statements that could change the workspace.
var ErrNotReadOnly = errors.New("only read-only queries are allowed: SELECT, WITH, FROM, DESCRIBE, SHOW, SUMMARIZE, EXPLAIN")

// SQL runs one read-only statement against the workspace database: tables
// log_lines, objects and events, and the view logs. It runs inside a
// transaction that is always rolled back, and the database has no access to
// the local file system.
func (w *Workspace) SQL(query string, limit int) (*SQLResult, error) {
	query = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	if !sqlStart.MatchString(query) || strings.Contains(query, ";") {
		return nil, ErrNotReadOnly
	}
	if limit <= 0 {
		limit = 1000
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := w.ldb.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	res := &SQLResult{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		if len(res.Rows) >= limit {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			vals[i] = jsonable(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	res.Millis = time.Since(start).Milliseconds()
	return res, rows.Err()
}

// mapValue converts the shapes the driver uses for MAP columns.
func mapValue(v any) map[string]any {
	switch t := v.(type) {
	case duckdb.Map:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = x
		}
		return m
	case duckdb.OrderedMap:
		return mapValue(&t)
	case *duckdb.OrderedMap:
		m := make(map[string]any, t.Len())
		for _, k := range t.Keys() {
			x, _ := t.Get(k)
			m[fmt.Sprint(k)] = x
		}
		return m
	}
	return nil
}

func jsonable(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case duckdb.Map, duckdb.OrderedMap, *duckdb.OrderedMap:
		m := map[string]any{}
		for k, x := range mapValue(t) {
			m[k] = jsonable(x)
		}
		return m
	case []any:
		for i := range t {
			t[i] = jsonable(t[i])
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = jsonable(x)
		}
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		if _, err := json.Marshal(t); err != nil {
			return fmt.Sprint(t)
		}
		return t
	}
}
