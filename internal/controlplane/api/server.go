package api

import (
	"log/slog"
	"net/http"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
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

type Server struct {
	cfg   Config
	store *store.Store
	clock clock.Clock
	log   *slog.Logger
}

func New(cfg Config, st *store.Store, clk clock.Clock, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, clock: clk, log: log}
}

func (s *Server) Register(mux *http.ServeMux) {
	auth := func(h http.HandlerFunc) http.Handler { return httpx.RequireBearer(s.cfg.AdminToken, h) }

	mux.Handle("POST /v1/devices/batch", auth(s.handleUpsertDevices))
	mux.Handle("GET /v1/devices/count", auth(s.handleCountDevices))
	mux.Handle("DELETE /v1/devices", auth(s.handleDeleteDevices))

	mux.Handle("POST /v1/campaigns", auth(s.handleCreateCampaign))
	mux.Handle("GET /v1/campaigns", auth(s.handleListCampaigns))
	mux.Handle("GET /v1/campaigns/{id}", auth(s.handleGetCampaign))
	mux.Handle("GET /v1/campaigns/{id}/ticks", auth(s.handleListTicks))
	for _, action := range []string{"start", "pause", "resume", "abort"} {
		mux.Handle("POST /v1/campaigns/{id}/"+action, auth(s.handleNotImplemented))
	}
}

func (s *Server) handleNotImplemented(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "not_implemented", "campaign lifecycle is not available yet")
}
