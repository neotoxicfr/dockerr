package store

import (
	"context"
	"encoding/json"
	"time"
)

// Operations are kept this long.
const opsRetention = 90 * 24 * time.Hour

// OpTask is one line of compose output: a resource (ID, e.g. "Container
// web-1"), what happened to it (Text) and its status.
type OpTask struct {
	ID, Text, Details, Status string
}

// Operation is a finished stack operation and its full compose output.
type Operation struct {
	ID, Host, Stack, Service, Kind, User string
	Started, Ended                       time.Time
	Err                                  string
	Tasks                                []OpTask
}

func (o Operation) Duration() time.Duration { return o.Ended.Sub(o.Started).Round(time.Second) }

// SaveOp records a finished operation and drops those past retention.
func (s *Store) SaveOp(ctx context.Context, o Operation) error {
	tasks, err := json.Marshal(o.Tasks)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO operations
		(id, host, stack, service, kind, user, started_at, ended_at, err, tasks) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.Host, o.Stack, o.Service, o.Kind, o.User, o.Started.UnixMilli(), o.Ended.UnixMilli(), o.Err, string(tasks))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM operations WHERE started_at < ?`, time.Now().Add(-opsRetention).UnixMilli())
	return err
}

// Ops returns the latest operations, newest first; stack "" lists all.
func (s *Store) Ops(ctx context.Context, stack string, n int) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, host, stack, service, kind, user, started_at, ended_at, err, tasks
		FROM operations WHERE ? = '' OR stack = ? ORDER BY started_at DESC LIMIT ?`, stack, stack, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var o Operation
		var a, b int64
		var tasks string
		if err := rows.Scan(&o.ID, &o.Host, &o.Stack, &o.Service, &o.Kind, &o.User, &a, &b, &o.Err, &tasks); err != nil {
			return nil, err
		}
		o.Started, o.Ended = time.UnixMilli(a), time.UnixMilli(b)
		_ = json.Unmarshal([]byte(tasks), &o.Tasks)
		out = append(out, o)
	}
	return out, rows.Err()
}
