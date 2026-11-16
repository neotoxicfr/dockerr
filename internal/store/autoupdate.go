package store

import (
	"context"
	"fmt"
	"time"
)

// AutoUpdates lists the stacks opted in to automatic updates on the local host.
func (s *Store) AutoUpdates(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stack FROM auto_update WHERE host = '' ORDER BY stack`)
	if err != nil {
		return nil, fmt.Errorf("lecture des mises à jour automatiques : %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetAutoUpdate opts a stack in or out of automatic updates.
func (s *Store) SetAutoUpdate(ctx context.Context, stack string, on bool, by string) error {
	var err error
	if on {
		_, err = s.db.ExecContext(ctx, `INSERT INTO auto_update (stack, host, created_by, created_at) VALUES (?, '', ?, ?) ON CONFLICT (stack, host) DO NOTHING`,
			stack, by, time.Now().Unix())
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM auto_update WHERE stack = ? AND host = ''`, stack)
	}
	if err != nil {
		return fmt.Errorf("réglage de la mise à jour automatique : %w", err)
	}
	return nil
}
