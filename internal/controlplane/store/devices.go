package store

import (
	"context"
	"fmt"
	"time"
)

type Device struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
	AppBuild int    `json:"app_build"`
}

func (s *Store) UpsertDevices(ctx context.Context, devs []Device, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin upsert devices: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO devices (token, platform, app_build, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET platform = excluded.platform, app_build = excluded.app_build`)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert devices: %w", err)
	}
	defer stmt.Close()
	for _, d := range devs {
		if _, err := stmt.ExecContext(ctx, d.Token, d.Platform, d.AppBuild, ms(now)); err != nil {
			return 0, fmt.Errorf("upsert device %s: %w", d.Token, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit upsert devices: %w", err)
	}
	return len(devs), nil
}

func (s *Store) CountDevices(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count devices: %w", err)
	}
	return n, nil
}

func (s *Store) DeleteAllDevices(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices`)
	if err != nil {
		return 0, fmt.Errorf("delete devices: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted devices: %w", err)
	}
	return n, nil
}

func (s *Store) AllDeviceTokens(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token FROM devices ORDER BY token`)
	if err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan device token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}
