package store

import (
	"context"
	"fmt"
	"time"
)

// LogAlert notifies when a container's log line matches Pattern.
type LogAlert struct {
	ID      int64
	Pattern string // text (any case), or /regexp/
	Stack   string // "" = every stack
	By      string
	Created time.Time
}

func (s *Store) LogAlerts(ctx context.Context) ([]LogAlert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, pattern, stack, created_by, created_at FROM log_alerts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("lecture des alertes : %w", err)
	}
	defer rows.Close()
	var out []LogAlert
	for rows.Next() {
		var a LogAlert
		var t int64
		if err := rows.Scan(&a.ID, &a.Pattern, &a.Stack, &a.By, &t); err != nil {
			return nil, err
		}
		a.Created = time.Unix(t, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AddLogAlert(ctx context.Context, a LogAlert) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO log_alerts (pattern, stack, created_by, created_at) VALUES (?, ?, ?, ?)`,
		a.Pattern, a.Stack, a.By, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("ajout de l'alerte : %w", err)
	}
	return nil
}

func (s *Store) DeleteLogAlert(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM log_alerts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("suppression de l'alerte : %w", err)
	}
	return nil
}
