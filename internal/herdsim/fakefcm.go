package herdsim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
)

type fcmRequest struct {
	Message struct {
		Token string            `json:"token"`
		Data  map[string]string `json:"data"`
	} `json:"message"`
}

type fcmError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func writeFCMError(w http.ResponseWriter, code int, msg, status string) {
	var e fcmError
	e.Error.Code, e.Error.Message, e.Error.Status = code, msg, status
	httpx.WriteJSON(w, code, e)
}

type rpsLimiter struct {
	mu     sync.Mutex
	second int64
	count  int
}

func (l *rpsLimiter) allow(now time.Time, max int) bool {
	if max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if sec := now.Unix(); sec != l.second {
		l.second, l.count = sec, 0
	}
	if l.count >= max {
		return false
	}
	l.count++
	return true
}

func (s *Sim) fcmHandler() http.HandlerFunc {
	var limiter rpsLimiter
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("action") != "messages:send" {
			http.NotFound(w, r)
			return
		}
		var req fcmRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Message.Token == "" {
			writeFCMError(w, http.StatusBadRequest, "invalid token", "INVALID_ARGUMENT")
			return
		}
		d, ok := s.byToken[req.Message.Token]
		if !ok {
			writeFCMError(w, http.StatusNotFound, "Requested entity was not found.", "NOT_FOUND")
			return
		}
		if !limiter.allow(s.clk.Now(), s.cfg.FakeFCMMaxRPS) {
			w.Header().Set("Retry-After", "1")
			writeFCMError(w, http.StatusTooManyRequests, "Quota exceeded.", "RESOURCE_EXHAUSTED")
			return
		}
		if s.cfg.FakeFCMLatency > 0 {
			_ = s.sleep(r.Context(), s.cfg.FakeFCMLatency)
		}
		var id [6]byte
		_, _ = rand.Read(id[:])
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"name": "projects/" + r.PathValue("project") + "/messages/" + hex.EncodeToString(id[:])})
		go s.onPush(d, req.Message.Data["campaign_id"])
	}
}
