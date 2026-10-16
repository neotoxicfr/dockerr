// Package store keeps dockerr's own state in SQLite: compose file history,
// user preferences and the exec audit log.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "dockerr.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	p, err := goose.NewProvider(goose.DialectSQLite3, db, mustSub(migrations, "migrations"))
	if err != nil {
		return nil, err
	}
	if _, err := p.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Version struct {
	ID           int64
	Stack, File  string
	Content      string
	Author, Note string
	Created      time.Time
}

// SaveVersion records content unless it equals the latest version.
func (s *Store) SaveVersion(ctx context.Context, v Version) error {
	var last string
	err := s.db.QueryRowContext(ctx, `SELECT content FROM versions WHERE stack = ? AND file = ? ORDER BY id DESC LIMIT 1`, v.Stack, v.File).Scan(&last)
	if err == nil && last == v.Content {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO versions (stack, file, content, author, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		v.Stack, v.File, v.Content, v.Author, v.Note, time.Now().Unix())
	return err
}

// Versions lists a stack's versions, newest first, without content.
func (s *Store) Versions(ctx context.Context, stack string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file, author, note, created_at FROM versions WHERE stack = ? ORDER BY id DESC LIMIT 200`, stack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v := Version{Stack: stack}
		var ts int64
		if err := rows.Scan(&v.ID, &v.File, &v.Author, &v.Note, &ts); err != nil {
			return nil, err
		}
		v.Created = time.Unix(ts, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Version(ctx context.Context, stack string, id int64) (Version, error) {
	v := Version{ID: id, Stack: stack}
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT file, content, author, note, created_at FROM versions WHERE stack = ? AND id = ?`, stack, id).
		Scan(&v.File, &v.Content, &v.Author, &v.Note, &ts)
	v.Created = time.Unix(ts, 0)
	return v, err
}

// PreviousVersion returns the version of the same file just before id.
func (s *Store) PreviousVersion(ctx context.Context, v Version) (Version, error) {
	p := Version{Stack: v.Stack, File: v.File}
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT id, content, author, note, created_at FROM versions WHERE stack = ? AND file = ? AND id < ? ORDER BY id DESC LIMIT 1`, v.Stack, v.File, v.ID).
		Scan(&p.ID, &p.Content, &p.Author, &p.Note, &ts)
	p.Created = time.Unix(ts, 0)
	return p, err
}

func (s *Store) Pref(ctx context.Context, user, key string) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM prefs WHERE user = ? AND key = ?`, user, key).Scan(&v)
	return v
}

func (s *Store) SetPref(ctx context.Context, user, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO prefs (user, key, value) VALUES (?, ?, ?) ON CONFLICT (user, key) DO UPDATE SET value = excluded.value`, user, key, value)
	return err
}

func (s *Store) ExecStart(ctx context.Context, user, container, cmd string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO exec_log (user, container, cmd, started_at) VALUES (?, ?, ?, ?)`, user, container, cmd, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ExecEnd(ctx context.Context, id int64) {
	_, _ = s.db.ExecContext(ctx, `UPDATE exec_log SET ended_at = ? WHERE id = ?`, time.Now().Unix(), id)
}

type ExecEntry struct {
	User, Container, Cmd string
	Started, Ended       time.Time
}

func (s *Store) ExecLog(ctx context.Context, n int) ([]ExecEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user, container, cmd, started_at, COALESCE(ended_at, 0) FROM exec_log ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecEntry
	for rows.Next() {
		var e ExecEntry
		var a, b int64
		if err := rows.Scan(&e.User, &e.Container, &e.Cmd, &a, &b); err != nil {
			return nil, err
		}
		e.Started = time.Unix(a, 0)
		if b > 0 {
			e.Ended = time.Unix(b, 0)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
