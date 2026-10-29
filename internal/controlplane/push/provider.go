package push

import (
	"context"
	"time"
)

type Message struct {
	Token      string
	Title      string
	Body       string
	CampaignID string
	ExpiresAt  time.Time
}

type Result struct {
	OK         bool
	Permanent  bool
	RetryAfter time.Duration
	Err        string
}

type Provider interface {
	Send(ctx context.Context, m Message) Result
}
