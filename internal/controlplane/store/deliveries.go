package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type DeliveryResult struct {
	Token     string
	OK        bool
	Permanent bool
	Err       string
}

func (s *Store) MarkStarted(ctx context.Context, id string, tokens []string, startedAt, deadlineAt time.Time, baselineRPS float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin start of %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO campaign_deliveries (campaign_id, token, state) VALUES (?, ?, 'pending')`)
	if err != nil {
		return fmt.Errorf("prepare deliveries of %s: %w", id, err)
	}
	defer stmt.Close()
	for _, t := range tokens {
		if _, err := stmt.ExecContext(ctx, id, t); err != nil {
			return fmt.Errorf("queue delivery %s/%s: %w", id, t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE campaigns SET status = 'running', started_at = ?, deadline_at = ?, baseline_rps = ?,
			targeted = (SELECT COUNT(*) FROM campaign_deliveries WHERE campaign_id = ?)
		WHERE id = ?`, ms(startedAt), ms(deadlineAt), baselineRPS, id, id)
	if err != nil {
		return fmt.Errorf("mark %s started: %w", id, err)
	}
	if err := requireRow(res, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit start of %s: %w", id, err)
	}
	return nil
}

func (s *Store) FetchPending(ctx context.Context, id string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token FROM campaign_deliveries WHERE campaign_id = ? AND state = 'pending' ORDER BY rowid LIMIT ?`,
		id, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch pending of %s: %w", id, err)
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan pending token of %s: %w", id, err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// Rows no longer pending are skipped, so a duplicate result cannot move the campaign counters twice.
func (s *Store) ApplyResults(ctx context.Context, id string, results []DeliveryResult, maxAttempts int, now time.Time) (sentDelta, failedDelta int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin results of %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range results {
		switch {
		case r.OK:
			res, err := tx.ExecContext(ctx, `
				UPDATE campaign_deliveries SET state = 'sent', attempts = attempts + 1, sent_at = ?
				WHERE campaign_id = ? AND token = ? AND state = 'pending'`, ms(now), id, r.Token)
			if err != nil {
				return 0, 0, fmt.Errorf("record sent %s/%s: %w", id, r.Token, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				sentDelta++
			}
		case r.Permanent:
			res, err := tx.ExecContext(ctx, `
				UPDATE campaign_deliveries SET state = 'failed', attempts = attempts + 1, last_error = ?
				WHERE campaign_id = ? AND token = ? AND state = 'pending'`, r.Err, id, r.Token)
			if err != nil {
				return 0, 0, fmt.Errorf("record failure %s/%s: %w", id, r.Token, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				failedDelta++
			}
		default:
			var state string
			err := tx.QueryRowContext(ctx, `
				UPDATE campaign_deliveries
				SET attempts = attempts + 1, last_error = ?,
					state = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END
				WHERE campaign_id = ? AND token = ? AND state = 'pending'
				RETURNING state`, r.Err, maxAttempts, id, r.Token).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return 0, 0, fmt.Errorf("record retry %s/%s: %w", id, r.Token, err)
			}
			if state == "failed" {
				failedDelta++
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE campaigns SET sent = sent + ?, failed = failed + ? WHERE id = ?`,
		sentDelta, failedDelta, id); err != nil {
		return 0, 0, fmt.Errorf("update counters of %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit results of %s: %w", id, err)
	}
	return sentDelta, failedDelta, nil
}

func (s *Store) CountPending(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM campaign_deliveries WHERE campaign_id = ? AND state = 'pending'`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending of %s: %w", id, err)
	}
	return n, nil
}

func (s *Store) ExpirePending(ctx context.Context, id string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin expiry of %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE campaign_deliveries SET state = 'expired' WHERE campaign_id = ? AND state = 'pending'`, id)
	if err != nil {
		return 0, fmt.Errorf("expire deliveries of %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count expired of %s: %w", id, err)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE campaigns SET expired = expired + ?, status = 'expired', finished_at = ? WHERE id = ?`, n, ms(now), id)
	if err != nil {
		return 0, fmt.Errorf("mark %s expired: %w", id, err)
	}
	if err := requireRow(res, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit expiry of %s: %w", id, err)
	}
	return n, nil
}
