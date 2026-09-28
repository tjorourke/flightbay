package store

import (
	"strings"

	"github.com/tjorourke/flightbay/internal/logs"
)

// LineLoc places a log line in the bundle: its chunk member and its position
// among the chunk's non-empty lines, which is how a review plan names it.
type LineLoc struct {
	ID    int64  `json:"id"`
	Chunk string `json:"chunk"`
	Index int    `json:"index"`
	Raw   string `json:"raw"`
}

// LineLocations returns where the lines matching a log filter live, at most
// max of them, and how many match in total.
func (w *Workspace) LineLocations(f LogFilter, max int) ([]LineLoc, int, error) {
	q, err := logs.ParseQuery(f.Query)
	if err != nil {
		return nil, 0, err
	}
	w.mu.RLock()
	ref := w.logEnd
	w.mu.RUnlock()
	where, args := q.SQL(ref)
	for _, fc := range []struct{ col, v string }{{"cluster", f.Cluster}, {"namespace", f.Namespace}, {"pod", f.Pod}, {"container", f.Container}, {"level", f.Level}, {"stream", f.Stream}} {
		if fc.v != "" {
			where += " AND " + fc.col + " = ?"
			args = append(args, fc.v)
		}
	}
	if f.Workload != "" {
		if k, n, ok := strings.Cut(f.Workload, "/"); ok {
			where += " AND workload_kind = ? AND workload = ?"
			args = append(args, k, n)
		} else {
			where += " AND workload = ?"
			args = append(args, f.Workload)
		}
	}
	if !f.From.IsZero() {
		where += " AND ts >= ?"
		args = append(args, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		where += " AND ts <= ?"
		args = append(args, f.To.UnixNano())
	}
	var total int
	if err := w.ldb.db.QueryRow("SELECT count(*) FROM log_lines WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := w.ldb.db.Query(`SELECT l.id, l.chunk, l.id - (SELECT min(m.id) FROM log_lines m WHERE m.chunk = l.chunk), l.raw
	  FROM log_lines l WHERE `+where+` ORDER BY l.id LIMIT ?`, append(args, max)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LineLoc
	for rows.Next() {
		var l LineLoc
		if err := rows.Scan(&l.ID, &l.Chunk, &l.Index, &l.Raw); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// LineLocation returns one line by id.
func (w *Workspace) LineLocation(id int64) (*LineLoc, error) {
	var l LineLoc
	err := w.ldb.db.QueryRow(`SELECT l.id, l.chunk, l.id - (SELECT min(m.id) FROM log_lines m WHERE m.chunk = l.chunk), l.raw FROM log_lines l WHERE l.id = ?`, id).
		Scan(&l.ID, &l.Chunk, &l.Index, &l.Raw)
	if err != nil {
		return nil, err
	}
	return &l, nil
}
