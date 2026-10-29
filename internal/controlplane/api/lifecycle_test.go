package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/dispatcher"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

type fakeLifecycle struct {
	st  *store.Store
	err error
}

func (f fakeLifecycle) move(ctx context.Context, id string, to campaign.Status) (*campaign.Campaign, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := f.st.SetStatus(ctx, id, to, nil); err != nil {
		return nil, err
	}
	return f.st.GetCampaign(ctx, id)
}

func (f fakeLifecycle) Start(ctx context.Context, id string) (*campaign.Campaign, error) {
	return f.move(ctx, id, campaign.StatusRunning)
}
func (f fakeLifecycle) Pause(ctx context.Context, id string) (*campaign.Campaign, error) {
	return f.move(ctx, id, campaign.StatusPaused)
}
func (f fakeLifecycle) Resume(ctx context.Context, id string) (*campaign.Campaign, error) {
	return f.move(ctx, id, campaign.StatusRunning)
}
func (f fakeLifecycle) Abort(ctx context.Context, id string) (*campaign.Campaign, error) {
	return f.move(ctx, id, campaign.StatusAborted)
}
func (fakeLifecycle) Live(string) (float64, float64, float64) { return 120, 200, 1.9 }

type fakeHealth struct {
	snap  guard.Snapshot
	at    time.Time
	stale bool
}

func (h fakeHealth) Latest() (guard.Snapshot, time.Time, bool) { return h.snap, h.at, h.stale }

func TestLifecycleEndpoints(t *testing.T) {
	f := newFixture(t)
	f.server.SetLifecycle(fakeLifecycle{st: f.store})
	created := decode[campaign.View](t, f.do("POST", "/v1/campaigns", createBody, true))

	tests := []struct {
		action string
		want   campaign.Status
	}{
		{"start", campaign.StatusRunning},
		{"pause", campaign.StatusPaused},
		{"resume", campaign.StatusRunning},
		{"abort", campaign.StatusAborted},
	}
	for _, tt := range tests {
		rec := f.do("POST", "/v1/campaigns/"+created.ID+"/"+tt.action, "", true)
		v := decode[campaign.View](t, rec)
		t.Logf("%s: %d status=%s rate=%v cap=%v amp=%v", tt.action, rec.Code, v.Status, v.CurrentRate, v.RateCap, v.Amplification)
		if rec.Code != http.StatusOK || v.Status != tt.want || v.CurrentRate != 120 || v.RateCap != 200 || v.Amplification != 1.9 {
			t.Fatalf("%s: status %d view %+v", tt.action, rec.Code, v)
		}
	}
	if rec := f.do("POST", "/v1/campaigns/"+created.ID+"/start", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("start without token: %d, want 401", rec.Code)
	}
}

func TestLifecycleErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		slug string
	}{
		{"not found", fmt.Errorf("campaign x: %w", store.ErrNotFound), 404, "not_found"},
		{"conflict", fmt.Errorf("draft -> paused: %w", dispatcher.ErrInvalidTransition), 409, "conflict"},
		{"internal", fmt.Errorf("disk on fire"), 500, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.SetLifecycle(fakeLifecycle{st: f.store, err: tt.err})
			rec := f.do("POST", "/v1/campaigns/cmp_x/pause", "", true)
			t.Logf("%d %s", rec.Code, rec.Body.String())
			if rec.Code != tt.code {
				t.Fatalf("status %d, want %d", rec.Code, tt.code)
			}
			if tt.slug != "" && errorCode(t, rec) != tt.slug {
				t.Fatalf("code %q, want %q", errorCode(t, rec), tt.slug)
			}
		})
	}
}

func TestTargetHealth(t *testing.T) {
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		health      HealthSource
		stale       bool
		hasSnapshot bool
	}{
		{"fresh", fakeHealth{snap: guard.Snapshot{Mode: "NORMAL", Headroom: 0.9}, at: at}, false, true},
		{"stale", fakeHealth{snap: guard.Snapshot{Mode: "NORMAL"}, at: at, stale: true}, true, true},
		{"never fetched", fakeHealth{stale: true}, true, false},
		{"no source", nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.health != nil {
				f.server.SetHealth(tt.health)
			}
			rec := f.do("GET", "/v1/target/health", "", true)
			got := decode[targetHealthResponse](t, rec)
			t.Logf("%d %s", rec.Code, rec.Body.String())
			if rec.Code != 200 || got.Stale != tt.stale || (got.Snapshot != nil) != tt.hasSnapshot {
				t.Fatalf("got %+v, want stale=%v snapshot=%v", got, tt.stale, tt.hasSnapshot)
			}
		})
	}
	if rec := newFixture(t).do("GET", "/v1/target/health", "", false); rec.Code != 401 {
		t.Fatalf("without token: %d, want 401", rec.Code)
	}
}
