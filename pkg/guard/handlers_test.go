package guard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

func adminDo(t *testing.T, g *Guard, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	g.RegisterHandlers(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeSnapshot(t *testing.T, rec *httptest.ResponseRecorder) Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode snapshot from %q: %v", rec.Body.String(), err)
	}
	return s
}

func TestAdminAuth(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	tests := []struct {
		name, method, path, token string
	}{
		{"health without token", "GET", "/guard/health", ""},
		{"health wrong token", "GET", "/guard/health", "nope"},
		{"override wrong token", "POST", "/guard/override", "nope"},
		{"reset without token", "POST", "/guard/reset", ""},
	}
	for _, tc := range tests {
		rec := adminDo(t, g, tc.method, tc.path, tc.token, `{"mode":"SHED"}`)
		t.Logf("%s: status %d", tc.name, rec.Code)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", tc.name, rec.Code)
		}
	}
	if rec := adminDo(t, g, "GET", "/guard/health", testToken, ""); rec.Code != 200 {
		t.Errorf("health with valid token: status %d, want 200", rec.Code)
	}
}

func TestAdminRejectsEverythingWhenTokenUnset(t *testing.T) {
	g, _ := newTestGuard(t, func(c *Config) { c.AdminToken = "" })
	mux := http.NewServeMux()
	g.RegisterHandlers(mux)
	req := httptest.NewRequest("GET", "/guard/health", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestOverrideLifecycle(t *testing.T) {
	g, clk := newTestGuard(t, nil)
	rec := adminDo(t, g, "POST", "/guard/override", testToken, `{"mode":"BROWNOUT","ttl_s":60}`)
	s := decodeSnapshot(t, rec)
	t.Logf("after override: status %d mode %s override_active %v", rec.Code, s.Mode, s.OverrideActive)
	if rec.Code != 200 || s.Mode != "BROWNOUT" || !s.OverrideActive || s.OverrideUntil == nil {
		t.Fatalf("status %d snapshot %+v, want 200 BROWNOUT active", rec.Code, s)
	}

	clk.Advance(61 * time.Second)
	g.Evaluate()
	s = g.Snapshot()
	t.Logf("61 s later: mode %s override_active %v", s.Mode, s.OverrideActive)
	if s.OverrideActive || s.Mode != "NORMAL" {
		t.Fatalf("override not cleared: %+v", s)
	}
}

func TestOverrideDefaultTTLAndClear(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	s := decodeSnapshot(t, adminDo(t, g, "POST", "/guard/override", testToken, `{"mode":"ELEVATED"}`))
	if s.OverrideUntil == nil || s.OverrideUntil.Sub(s.At) != 300*time.Second {
		t.Fatalf("default ttl: override_until %v at %v, want 300 s apart", s.OverrideUntil, s.At)
	}
	rec := adminDo(t, g, "DELETE", "/guard/override", testToken, "")
	s = decodeSnapshot(t, rec)
	if rec.Code != 200 || s.OverrideActive || s.Mode != "NORMAL" {
		t.Fatalf("after DELETE: status %d snapshot %+v", rec.Code, s)
	}
}

func TestOverrideValidation(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	tests := []struct{ name, body string }{
		{"unknown mode", `{"mode":"PANIC"}`},
		{"ttl too large", `{"mode":"SHED","ttl_s":4000}`},
		{"negative ttl", `{"mode":"SHED","ttl_s":-1}`},
		{"unknown field", `{"mode":"SHED","extra":1}`},
		{"empty body", ``},
	}
	for _, tc := range tests {
		rec := adminDo(t, g, "POST", "/guard/override", testToken, tc.body)
		t.Logf("%s: status %d", tc.name, rec.Code)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.name, rec.Code)
		}
	}
	if s := g.Snapshot(); s.OverrideActive {
		t.Errorf("a rejected request left an override active")
	}
}

func TestEnforceToggle(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	s := decodeSnapshot(t, adminDo(t, g, "POST", "/guard/enforce", testToken, `{"enabled":false}`))
	if s.Enforce {
		t.Fatalf("enforce = true after disabling")
	}
	if rec := adminDo(t, g, "POST", "/guard/enforce", testToken, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: status %d, want 400", rec.Code)
	}
}

func TestPolicyEndpointIsPublic(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	rec := adminDo(t, g, "GET", "/v1/policy", "", "")
	var p policy.JSON
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	t.Logf("GET /v1/policy -> %d %s", rec.Code, p.Header)
	if rec.Code != 200 || p.Header != policy.For(policy.ModeNormal).Header() {
		t.Fatalf("status %d header %q, want 200 and the NORMAL header", rec.Code, p.Header)
	}

	g.SetOverride(policy.ModeShed, time.Minute)
	g.SetEnforce(false)
	rec = adminDo(t, g, "GET", "/v1/policy", "", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Mode != "NORMAL" {
		t.Fatalf("shadow mode policy = %s, want NORMAL", p.Mode)
	}
}

func TestResetClearsState(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	serve(g.Middleware(okHandler(nil)), "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: "cmp_0123456789ab"})
	g.SetOverride(policy.ModeShed, time.Minute)
	g.Evaluate()
	if n := len(g.Snapshot().CampaignRequests); n != 1 {
		t.Fatalf("campaigns before reset = %d, want 1", n)
	}

	rec := adminDo(t, g, "POST", "/guard/reset", testToken, "")
	s := g.Snapshot()
	t.Logf("reset: status %d, campaigns %v, mode %s", rec.Code, s.CampaignRequests, s.Mode)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	if len(s.CampaignRequests) != 0 || s.OverrideActive || s.Mode != "NORMAL" {
		t.Fatalf("state not cleared: %+v", s)
	}
}
