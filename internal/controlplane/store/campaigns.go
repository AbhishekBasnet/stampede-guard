package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
)

const campaignColumns = `id, name, title, body, audience_json, pacing, static_rate, min_rate, max_rate,
	deadline_seconds, amplification_prior, status, targeted, sent, failed, expired, baseline_rps,
	final_amplification, created_at, started_at, deadline_at, finished_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanCampaign(row rowScanner) (*campaign.Campaign, error) {
	var (
		c                           campaign.Campaign
		audience                    string
		created                     int64
		started, deadline, finished sql.NullInt64
	)
	err := row.Scan(&c.ID, &c.Name, &c.Title, &c.Body, &audience, &c.Pacing, &c.StaticRate, &c.MinRate,
		&c.MaxRate, &c.DeadlineSeconds, &c.AmplificationPrior, &c.Status, &c.Targeted, &c.Sent, &c.Failed,
		&c.Expired, &c.BaselineRPS, &c.FinalAmplification, &created, &started, &deadline, &finished)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(audience), &c.Audience); err != nil {
		return nil, fmt.Errorf("decode audience of %s: %w", c.ID, err)
	}
	c.CreatedAt = fromMs(created)
	c.StartedAt, c.DeadlineAt, c.FinishedAt = timePtr(started), timePtr(deadline), timePtr(finished)
	return &c, nil
}

func (s *Store) CreateCampaign(ctx context.Context, c *campaign.Campaign) error {
	audience, err := json.Marshal(c.Audience)
	if err != nil {
		return fmt.Errorf("encode audience of %s: %w", c.ID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO campaigns (id, name, title, body, audience_json, pacing, static_rate, min_rate, max_rate,
			deadline_seconds, amplification_prior, status, targeted, sent, failed, expired, baseline_rps,
			final_amplification, created_at, started_at, deadline_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Title, c.Body, string(audience), c.Pacing, c.StaticRate, c.MinRate, c.MaxRate,
		c.DeadlineSeconds, c.AmplificationPrior, c.Status, c.Targeted, c.Sent, c.Failed, c.Expired,
		c.BaselineRPS, c.FinalAmplification, ms(c.CreatedAt), msPtr(c.StartedAt), msPtr(c.DeadlineAt),
		msPtr(c.FinishedAt))
	if err != nil {
		return fmt.Errorf("create campaign %s: %w", c.ID, err)
	}
	return nil
}

func (s *Store) GetCampaign(ctx context.Context, id string) (*campaign.Campaign, error) {
	c, err := scanCampaign(s.db.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("campaign %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign %s: %w", id, err)
	}
	return c, nil
}

func (s *Store) listCampaigns(ctx context.Context, query string, args ...any) ([]*campaign.Campaign, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer rows.Close()
	var out []*campaign.Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListCampaigns(ctx context.Context, limit int) ([]*campaign.Campaign, error) {
	return s.listCampaigns(ctx,
		`SELECT `+campaignColumns+` FROM campaigns ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
}

func (s *Store) ListCampaignsByStatus(ctx context.Context, st campaign.Status) ([]*campaign.Campaign, error) {
	return s.listCampaigns(ctx,
		`SELECT `+campaignColumns+` FROM campaigns WHERE status = ? ORDER BY created_at, rowid`, st)
}

// A nil finishedAt leaves any existing finished_at untouched.
func (s *Store) SetStatus(ctx context.Context, id string, st campaign.Status, finishedAt *time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE campaigns SET status = ?, finished_at = COALESCE(?, finished_at) WHERE id = ?`,
		st, msPtr(finishedAt), id)
	if err != nil {
		return fmt.Errorf("set status of %s: %w", id, err)
	}
	return requireRow(res, id)
}

func (s *Store) SetFinalAmplification(ctx context.Context, id string, v float64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE campaigns SET final_amplification = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("set final amplification of %s: %w", id, err)
	}
	return requireRow(res, id)
}

func (s *Store) LatestFinalAmplification(ctx context.Context) (float64, bool, error) {
	var v float64
	err := s.db.QueryRowContext(ctx, `
		SELECT final_amplification FROM campaigns
		WHERE final_amplification > 0 AND finished_at IS NOT NULL
		ORDER BY finished_at DESC, rowid DESC LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("latest final amplification: %w", err)
	}
	return v, true, nil
}

func requireRow(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check update of %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("campaign %s: %w", id, ErrNotFound)
	}
	return nil
}
