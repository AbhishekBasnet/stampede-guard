package api

import (
	"errors"
	"net/http"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/ids"
)

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req campaign.CreateRequest
	if err := httpx.ReadJSON(w, r, &req, maxBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.ApplyDefaults(s.cfg.DefaultMinRate, s.cfg.DefaultMaxRate)
	if err := req.Validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	c := campaign.New(req, ids.New("cmp"), s.clock.Now().UTC())
	if err := s.store.CreateCampaign(r.Context(), c); err != nil {
		s.internalError(w, "create campaign", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c.View(0, 0, 0))
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCampaign(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c.View(0, 0, 0))
}

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListCampaigns(r.Context(), listCampaignsLimit)
	if err != nil {
		s.internalError(w, "list campaigns", err)
		return
	}
	views := make([]campaign.View, 0, len(list))
	for _, c := range list {
		views = append(views, c.View(0, 0, 0))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"campaigns": views})
}

func (s *Server) handleListTicks(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCampaign(w, r)
	if !ok {
		return
	}
	ticks, err := s.store.ListTicks(r.Context(), c.ID)
	if err != nil {
		s.internalError(w, "list ticks", err)
		return
	}
	if ticks == nil {
		ticks = []store.Tick{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ticks": ticks})
}

func (s *Server) loadCampaign(w http.ResponseWriter, r *http.Request) (*campaign.Campaign, bool) {
	c, err := s.store.GetCampaign(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "campaign not found")
		return nil, false
	}
	if err != nil {
		s.internalError(w, "get campaign", err)
		return nil, false
	}
	return c, true
}
