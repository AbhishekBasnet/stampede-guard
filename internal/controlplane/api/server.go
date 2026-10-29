package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

const (
	maxBodyBytes       = 1 << 20
	maxDeviceBodyBytes = 5 << 20
	listCampaignsLimit = 50
)

type Config struct {
	AdminToken     string
	DefaultMinRate int
	DefaultMaxRate int
}

type Lifecycle interface {
	Start(ctx context.Context, id string) (*campaign.Campaign, error)
	Pause(ctx context.Context, id string) (*campaign.Campaign, error)
	Resume(ctx context.Context, id string) (*campaign.Campaign, error)
	Abort(ctx context.Context, id string) (*campaign.Campaign, error)
	Live(id string) (rate, rateCap, amplification float64)
}

type HealthSource interface {
	Latest() (snap guard.Snapshot, fetchedAt time.Time, stale bool)
}

type Server struct {
	cfg       Config
	store     *store.Store
	clock     clock.Clock
	log       *slog.Logger
	lifecycle Lifecycle
	health    HealthSource
}

func New(cfg Config, st *store.Store, clk clock.Clock, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, clock: clk, log: log}
}

func (s *Server) SetLifecycle(l Lifecycle) { s.lifecycle = l }

func (s *Server) SetHealth(h HealthSource) { s.health = h }

func (s *Server) Register(mux *http.ServeMux) {
	auth := func(h http.HandlerFunc) http.Handler { return httpx.RequireBearer(s.cfg.AdminToken, h) }

	mux.Handle("POST /v1/devices/batch", auth(s.handleUpsertDevices))
	mux.Handle("GET /v1/devices/count", auth(s.handleCountDevices))
	mux.Handle("DELETE /v1/devices", auth(s.handleDeleteDevices))

	mux.Handle("POST /v1/campaigns", auth(s.handleCreateCampaign))
	mux.Handle("GET /v1/campaigns", auth(s.handleListCampaigns))
	mux.Handle("GET /v1/campaigns/{id}", auth(s.handleGetCampaign))
	mux.Handle("GET /v1/campaigns/{id}/ticks", auth(s.handleListTicks))
	mux.Handle("POST /v1/campaigns/{id}/start", auth(s.lifecycleHandler(func(l Lifecycle) transition { return l.Start })))
	mux.Handle("POST /v1/campaigns/{id}/pause", auth(s.lifecycleHandler(func(l Lifecycle) transition { return l.Pause })))
	mux.Handle("POST /v1/campaigns/{id}/resume", auth(s.lifecycleHandler(func(l Lifecycle) transition { return l.Resume })))
	mux.Handle("POST /v1/campaigns/{id}/abort", auth(s.lifecycleHandler(func(l Lifecycle) transition { return l.Abort })))

	mux.Handle("GET /v1/target/health", auth(s.handleTargetHealth))
}

func (s *Server) handleNotImplemented(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "not_implemented", "campaign lifecycle is not available yet")
}
