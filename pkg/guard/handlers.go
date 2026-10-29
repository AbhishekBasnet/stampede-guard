package guard

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

const (
	maxBodyBytes       = 1 << 20
	defaultOverrideTTL = 300
	maxOverrideTTL     = 3600
)

func (g *Guard) RegisterHandlers(mux *http.ServeMux) {
	mux.Handle("GET /guard/health", g.admin(http.HandlerFunc(g.handleHealth)))
	mux.Handle("POST /guard/override", g.admin(http.HandlerFunc(g.handleSetOverride)))
	mux.Handle("DELETE /guard/override", g.admin(http.HandlerFunc(g.handleClearOverride)))
	mux.Handle("POST /guard/enforce", g.admin(http.HandlerFunc(g.handleEnforce)))
	mux.Handle("POST /guard/reset", g.admin(http.HandlerFunc(g.handleReset)))
	mux.HandleFunc("GET /v1/policy", g.handlePolicy)
}

func (g *Guard) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// An empty configured token must never authenticate anyone.
		if !ok || g.cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.cfg.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, g.Snapshot())
}

func (g *Guard) handleSetOverride(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
		TTLS int    `json:"ttl_s"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	mode, err := policy.ParseMode(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown mode "+req.Mode)
		return
	}
	if req.TTLS == 0 {
		req.TTLS = defaultOverrideTTL
	}
	if req.TTLS < 1 || req.TTLS > maxOverrideTTL {
		writeError(w, http.StatusBadRequest, "bad_request", "ttl_s must be between 1 and 3600")
		return
	}
	g.SetOverride(mode, time.Duration(req.TTLS)*time.Second)
	g.Evaluate()
	writeJSON(w, http.StatusOK, g.Snapshot())
}

func (g *Guard) handleClearOverride(w http.ResponseWriter, _ *http.Request) {
	g.ClearOverride()
	g.Evaluate()
	writeJSON(w, http.StatusOK, g.Snapshot())
}

func (g *Guard) handleEnforce(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "enabled is required")
		return
	}
	g.SetEnforce(*req.Enabled)
	g.Evaluate()
	writeJSON(w, http.StatusOK, g.Snapshot())
}

func (g *Guard) handleReset(w http.ResponseWriter, _ *http.Request) {
	g.Reset()
	g.Evaluate()
	w.WriteHeader(http.StatusNoContent)
}

func (g *Guard) handlePolicy(w http.ResponseWriter, _ *http.Request) {
	mode := g.EffectiveMode()
	if !g.enforce.Load() {
		mode = policy.ModeNormal
	}
	writeJSON(w, http.StatusOK, policy.For(mode).ToJSON())
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, struct {
		Error errorBody `json:"error"`
	}{errorBody{Code: code, Message: msg}})
}
