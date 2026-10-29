package api

import (
	"fmt"
	"net/http"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
)

const (
	maxDevicesPerBatch = 5000
	maxTokenLength     = 200
)

func validateDevices(devs []store.Device) error {
	if len(devs) < 1 || len(devs) > maxDevicesPerBatch {
		return fmt.Errorf("devices must contain 1 to %d entries", maxDevicesPerBatch)
	}
	for i, d := range devs {
		if len(d.Token) < 1 || len(d.Token) > maxTokenLength {
			return fmt.Errorf("devices[%d].token must be 1 to %d characters", i, maxTokenLength)
		}
		if d.Platform != "android" && d.Platform != "ios" {
			return fmt.Errorf("devices[%d].platform must be android or ios", i)
		}
	}
	return nil
}

func (s *Server) handleUpsertDevices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Devices []store.Device `json:"devices"`
	}
	if err := httpx.ReadJSON(w, r, &req, maxDeviceBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := validateDevices(req.Devices); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	n, err := s.store.UpsertDevices(r.Context(), req.Devices, s.clock.Now())
	if err != nil {
		s.internalError(w, "upsert devices", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"upserted": n})
}

func (s *Server) handleCountDevices(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountDevices(r.Context())
	if err != nil {
		s.internalError(w, "count devices", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"count": n})
}

func (s *Server) handleDeleteDevices(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.DeleteAllDevices(r.Context())
	if err != nil {
		s.internalError(w, "delete devices", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}
