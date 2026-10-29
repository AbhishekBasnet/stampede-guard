package campaign

import (
	"errors"
	"fmt"
	"hash/fnv"
	"time"
	"unicode/utf8"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusExpired   Status = "expired"
	StatusAborted   Status = "aborted"
)

type Pacing string

const (
	PacingUnlimited Pacing = "unlimited"
	PacingStatic    Pacing = "static"
	PacingAdaptive  Pacing = "adaptive"
)

const (
	AudienceAll    = "all"
	AudienceSample = "sample"

	DefaultDeadlineSeconds = 300
)

type Audience struct {
	Kind     string  `json:"kind"`
	Fraction float64 `json:"fraction,omitempty"`
}

type Campaign struct {
	ID, Name, Title, Body string
	Audience              Audience
	Pacing                Pacing
	StaticRate            int
	MinRate, MaxRate      int
	DeadlineSeconds       int
	AmplificationPrior    float64
	Status                Status
	Targeted, Sent        int64
	Failed, Expired       int64
	BaselineRPS           float64
	FinalAmplification    float64
	CreatedAt             time.Time
	StartedAt             *time.Time
	DeadlineAt            *time.Time
	FinishedAt            *time.Time
}

type CreateRequest struct {
	Name               string   `json:"name"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	Audience           Audience `json:"audience"`
	Pacing             Pacing   `json:"pacing"`
	StaticRate         int      `json:"static_rate"`
	MinRate            int      `json:"min_rate"`
	MaxRate            int      `json:"max_rate"`
	DeadlineSeconds    int      `json:"deadline_s"`
	AmplificationPrior float64  `json:"amplification_prior"`
}

type View struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Title              string     `json:"title"`
	Body               string     `json:"body"`
	Audience           Audience   `json:"audience"`
	Pacing             Pacing     `json:"pacing"`
	StaticRate         int        `json:"static_rate"`
	MinRate            int        `json:"min_rate"`
	MaxRate            int        `json:"max_rate"`
	DeadlineSeconds    int        `json:"deadline_s"`
	AmplificationPrior float64    `json:"amplification_prior"`
	Status             Status     `json:"status"`
	Targeted           int64      `json:"targeted"`
	Sent               int64      `json:"sent"`
	Failed             int64      `json:"failed"`
	Expired            int64      `json:"expired"`
	Pending            int64      `json:"pending"`
	CurrentRate        float64    `json:"current_rate"`
	RateCap            float64    `json:"rate_cap"`
	Amplification      float64    `json:"amplification"`
	BaselineRPS        float64    `json:"baseline_rps"`
	FinalAmplification float64    `json:"final_amplification"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	DeadlineAt         *time.Time `json:"deadline_at,omitempty"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

func (r *CreateRequest) ApplyDefaults(defMin, defMax int) {
	if r.MinRate == 0 {
		r.MinRate = defMin
	}
	if r.MaxRate == 0 {
		r.MaxRate = defMax
	}
	if r.DeadlineSeconds == 0 {
		r.DeadlineSeconds = DefaultDeadlineSeconds
	}
}

func (r CreateRequest) Validate() error {
	switch {
	case utf8.RuneCountInString(r.Name) < 1 || utf8.RuneCountInString(r.Name) > 100:
		return errors.New("name must be 1 to 100 characters")
	case utf8.RuneCountInString(r.Title) < 1 || utf8.RuneCountInString(r.Title) > 100:
		return errors.New("title must be 1 to 100 characters")
	case utf8.RuneCountInString(r.Body) > 500:
		return errors.New("body must be at most 500 characters")
	}
	switch r.Audience.Kind {
	case AudienceAll:
	case AudienceSample:
		if r.Audience.Fraction <= 0 || r.Audience.Fraction > 1 {
			return errors.New("audience fraction must be greater than 0 and at most 1")
		}
	default:
		return fmt.Errorf("audience kind must be %q or %q", AudienceAll, AudienceSample)
	}
	switch r.Pacing {
	case PacingUnlimited, PacingAdaptive:
	case PacingStatic:
		if r.StaticRate < 1 {
			return errors.New("static_rate must be at least 1 for static pacing")
		}
	default:
		return fmt.Errorf("pacing must be %q, %q or %q", PacingUnlimited, PacingStatic, PacingAdaptive)
	}
	switch {
	case r.MinRate < 1 || r.MinRate > r.MaxRate || r.MaxRate > 100000:
		return errors.New("rates must satisfy 1 <= min_rate <= max_rate <= 100000")
	case r.DeadlineSeconds < 1 || r.DeadlineSeconds > 86400:
		return errors.New("deadline_s must be between 1 and 86400")
	case r.AmplificationPrior < 0 || r.AmplificationPrior > 100:
		return errors.New("amplification_prior must be between 0 and 100")
	}
	return nil
}

func New(r CreateRequest, id string, now time.Time) *Campaign {
	return &Campaign{
		ID:                 id,
		Name:               r.Name,
		Title:              r.Title,
		Body:               r.Body,
		Audience:           r.Audience,
		Pacing:             r.Pacing,
		StaticRate:         r.StaticRate,
		MinRate:            r.MinRate,
		MaxRate:            r.MaxRate,
		DeadlineSeconds:    r.DeadlineSeconds,
		AmplificationPrior: r.AmplificationPrior,
		Status:             StatusDraft,
		CreatedAt:          now,
	}
}

var transitions = map[Status][]Status{
	StatusDraft:   {StatusRunning, StatusAborted},
	StatusRunning: {StatusPaused, StatusCompleted, StatusExpired, StatusAborted},
	StatusPaused:  {StatusRunning, StatusExpired, StatusAborted},
}

func CanTransition(from, to Status) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Hashing the token keeps a sample stable across restarts and across repeated starts.
func InAudience(a Audience, token string) bool {
	switch a.Kind {
	case AudienceAll:
		return true
	case AudienceSample:
		h := fnv.New32a()
		_, _ = h.Write([]byte(token))
		return float64(h.Sum32())/(1<<32) < a.Fraction
	default:
		return false
	}
}

func (c *Campaign) View(currentRate, rateCap, amplification float64) View {
	return View{
		ID: c.ID, Name: c.Name, Title: c.Title, Body: c.Body, Audience: c.Audience, Pacing: c.Pacing,
		StaticRate: c.StaticRate, MinRate: c.MinRate, MaxRate: c.MaxRate, DeadlineSeconds: c.DeadlineSeconds,
		AmplificationPrior: c.AmplificationPrior, Status: c.Status,
		Targeted: c.Targeted, Sent: c.Sent, Failed: c.Failed, Expired: c.Expired,
		Pending:     c.Targeted - c.Sent - c.Failed - c.Expired,
		CurrentRate: currentRate, RateCap: rateCap, Amplification: amplification,
		BaselineRPS: c.BaselineRPS, FinalAmplification: c.FinalAmplification,
		CreatedAt: c.CreatedAt, StartedAt: c.StartedAt, DeadlineAt: c.DeadlineAt, FinishedAt: c.FinishedAt,
	}
}
