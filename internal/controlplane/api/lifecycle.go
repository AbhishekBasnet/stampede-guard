package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/dispatcher"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

type transition func(ctx context.Context, id string) (*campaign.Campaign, error)

func (s *Server) lifecycleHandler(pick func(Lifecycle) transition) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.lifecycle == nil {
			s.handleNotImplemented(w, r)
			return
		}
		c, err := pick(s.lifecycle)(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, store.ErrNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "campaign not found")
		case errors.Is(err, dispatcher.ErrInvalidTransition):
			httpx.WriteError(w, http.StatusConflict, "conflict", err.Error())
		case err != nil:
			s.internalError(w, "campaign transition", err)
		default:
			httpx.WriteJSON(w, http.StatusOK, s.view(c))
		}
	}
}

func (s *Server) view(c *campaign.Campaign) campaign.View {
	if s.lifecycle == nil {
		return c.View(0, 0, 0)
	}
	rate, rateCap, amp := s.lifecycle.Live(c.ID)
	return c.View(rate, rateCap, amp)
}

type targetHealthResponse struct {
	Stale     bool            `json:"stale"`
	FetchedAt *time.Time      `json:"fetched_at,omitempty"`
	Snapshot  *guard.Snapshot `json:"snapshot,omitempty"`
}

func (s *Server) handleTargetHealth(w http.ResponseWriter, _ *http.Request) {
	if s.health == nil {
		httpx.WriteJSON(w, http.StatusOK, targetHealthResponse{Stale: true})
		return
	}
	snap, at, stale := s.health.Latest()
	resp := targetHealthResponse{Stale: stale}
	if !at.IsZero() {
		resp.FetchedAt = &at
		resp.Snapshot = &snap
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
