package store

import (
	"context"
	"fmt"
	"time"
)

type Tick struct {
	CampaignID    string    `json:"campaign_id"`
	At            time.Time `json:"at"`
	Rate          float64   `json:"rate"`
	RateCap       float64   `json:"rate_cap"`
	Reason        string    `json:"reason"`
	SentTotal     int64     `json:"sent_total"`
	Headroom      float64   `json:"headroom"`
	Mode          string    `json:"mode"`
	P99Ms         float64   `json:"p99_ms"`
	Amplification float64   `json:"amplification"`
}

func (s *Store) InsertTick(ctx context.Context, t Tick) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pacer_ticks (campaign_id, at, rate, rate_cap, reason, sent_total, headroom, mode, p99_ms, amplification)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.CampaignID, ms(t.At), t.Rate, t.RateCap, t.Reason, t.SentTotal, t.Headroom, t.Mode, t.P99Ms, t.Amplification)
	if err != nil {
		return fmt.Errorf("insert tick of %s: %w", t.CampaignID, err)
	}
	return nil
}

func (s *Store) ListTicks(ctx context.Context, id string) ([]Tick, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT campaign_id, at, rate, rate_cap, reason, sent_total, headroom, mode, p99_ms, amplification
		FROM pacer_ticks WHERE campaign_id = ? ORDER BY at, rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("list ticks of %s: %w", id, err)
	}
	defer rows.Close()
	var out []Tick
	for rows.Next() {
		var t Tick
		var at int64
		if err := rows.Scan(&t.CampaignID, &at, &t.Rate, &t.RateCap, &t.Reason, &t.SentTotal, &t.Headroom,
			&t.Mode, &t.P99Ms, &t.Amplification); err != nil {
			return nil, fmt.Errorf("scan tick of %s: %w", id, err)
		}
		t.At = fromMs(at)
		out = append(out, t)
	}
	return out, rows.Err()
}
