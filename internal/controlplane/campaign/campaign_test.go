package campaign

import (
	"fmt"
	"testing"
	"time"
)

func validRequest() CreateRequest {
	r := CreateRequest{
		Name: "test", Title: "Hi", Body: "Hello",
		Audience: Audience{Kind: AudienceAll}, Pacing: PacingAdaptive,
	}
	r.ApplyDefaults(20, 5000)
	return r
}

func TestApplyDefaults(t *testing.T) {
	r := validRequest()
	t.Logf("defaults: min %d max %d deadline %d", r.MinRate, r.MaxRate, r.DeadlineSeconds)
	if r.MinRate != 20 || r.MaxRate != 5000 || r.DeadlineSeconds != 300 {
		t.Fatalf("defaults = %d %d %d, want 20 5000 300", r.MinRate, r.MaxRate, r.DeadlineSeconds)
	}
	r = CreateRequest{MinRate: 5, MaxRate: 50, DeadlineSeconds: 10}
	r.ApplyDefaults(20, 5000)
	if r.MinRate != 5 || r.MaxRate != 50 || r.DeadlineSeconds != 10 {
		t.Fatalf("explicit values overwritten: %+v", r)
	}
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("a valid request was rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateRequest)
	}{
		{"empty name", func(r *CreateRequest) { r.Name = "" }},
		{"name too long", func(r *CreateRequest) { r.Name = string(make([]rune, 101)) }},
		{"empty title", func(r *CreateRequest) { r.Title = "" }},
		{"body too long", func(r *CreateRequest) { r.Body = fmt.Sprintf("%501s", "x") }},
		{"unknown pacing", func(r *CreateRequest) { r.Pacing = "fast" }},
		{"static without rate", func(r *CreateRequest) { r.Pacing, r.StaticRate = PacingStatic, 0 }},
		{"min above max", func(r *CreateRequest) { r.MinRate, r.MaxRate = 100, 50 }},
		{"max above limit", func(r *CreateRequest) { r.MaxRate = 100001 }},
		{"zero deadline", func(r *CreateRequest) { r.DeadlineSeconds = 0 }},
		{"deadline too long", func(r *CreateRequest) { r.DeadlineSeconds = 86401 }},
		{"sample fraction 0", func(r *CreateRequest) { r.Audience = Audience{Kind: AudienceSample, Fraction: 0} }},
		{"sample fraction 1.5", func(r *CreateRequest) { r.Audience = Audience{Kind: AudienceSample, Fraction: 1.5} }},
		{"unknown audience", func(r *CreateRequest) { r.Audience = Audience{Kind: "everyone"} }},
		{"prior too high", func(r *CreateRequest) { r.AmplificationPrior = 101 }},
		{"negative prior", func(r *CreateRequest) { r.AmplificationPrior = -1 }},
	}
	for _, tc := range tests {
		r := validRequest()
		tc.mutate(&r)
		err := r.Validate()
		t.Logf("%s: %v", tc.name, err)
		if err == nil {
			t.Errorf("%s: Validate accepted %+v", tc.name, r)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	r := validRequest()
	r.Audience = Audience{Kind: AudienceSample, Fraction: 1}
	r.Pacing, r.StaticRate = PacingStatic, 50
	r.Body = ""
	if err := r.Validate(); err != nil {
		t.Fatalf("valid static sample request rejected: %v", err)
	}
}

func TestCanTransition(t *testing.T) {
	allowed := [][2]Status{
		{StatusDraft, StatusRunning},
		{StatusRunning, StatusPaused},
		{StatusPaused, StatusRunning},
		{StatusRunning, StatusCompleted},
		{StatusRunning, StatusExpired},
		{StatusPaused, StatusExpired},
		{StatusDraft, StatusAborted},
		{StatusRunning, StatusAborted},
		{StatusPaused, StatusAborted},
	}
	for _, p := range allowed {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	denied := [][2]Status{
		{StatusCompleted, StatusRunning},
		{StatusDraft, StatusPaused},
		{StatusDraft, StatusCompleted},
		{StatusExpired, StatusRunning},
		{StatusAborted, StatusRunning},
		{StatusPaused, StatusCompleted},
	}
	for _, p := range denied {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be denied", p[0], p[1])
		}
	}
}

func TestInAudienceSample(t *testing.T) {
	a := Audience{Kind: AudienceSample, Fraction: 0.5}
	count := func() int {
		n := 0
		for i := 0; i < 10000; i++ {
			if InAudience(a, fmt.Sprintf("dev-%06d", i)) {
				n++
			}
		}
		return n
	}
	first, second := count(), count()
	t.Logf("sample 0.5 over 10000 tokens: %d included", first)
	if first < 4700 || first > 5300 {
		t.Fatalf("included %d, want between 4700 and 5300", first)
	}
	if first != second {
		t.Fatalf("sampling is not deterministic: %d then %d", first, second)
	}
}

func TestInAudienceAllAndEdges(t *testing.T) {
	if !InAudience(Audience{Kind: AudienceAll}, "anything") {
		t.Errorf("kind all excluded a token")
	}
	if InAudience(Audience{Kind: "other"}, "x") {
		t.Errorf("unknown kind included a token")
	}
	for i := 0; i < 1000; i++ {
		if !InAudience(Audience{Kind: AudienceSample, Fraction: 1}, fmt.Sprintf("dev-%d", i)) {
			t.Fatalf("fraction 1 excluded dev-%d", i)
		}
	}
}

func TestNewAndView(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New(validRequest(), "cmp_0123456789ab", now)
	if c.Status != StatusDraft || !c.CreatedAt.Equal(now) || c.ID != "cmp_0123456789ab" {
		t.Fatalf("New = %+v", c)
	}
	c.Targeted, c.Sent, c.Failed, c.Expired = 100, 20, 5, 10
	v := c.View(12.5, 30, 1.4)
	if v.Pending != 65 || v.CurrentRate != 12.5 || v.RateCap != 30 || v.Amplification != 1.4 {
		t.Fatalf("View = %+v", v)
	}
}
