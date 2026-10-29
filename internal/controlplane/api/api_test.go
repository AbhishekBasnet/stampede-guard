package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

const token = "test-token"

type fixture struct {
	mux    *http.ServeMux
	store  *store.Store
	server *Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mux := http.NewServeMux()
	srv := New(Config{AdminToken: token, DefaultMinRate: 20, DefaultMaxRate: 5000}, st,
		clock.NewFake(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Register(mux)
	return &fixture{mux: mux, store: st, server: srv}
}

func (f *fixture) do(method, path, body string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[struct {
		Error struct{ Code string } `json:"error"`
	}](t, rec).Error.Code
}

const createBody = `{"name":"test","title":"Hi","body":"Hello","audience":{"kind":"all"},"pacing":"adaptive"}`

func TestAuthRequired(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/devices/batch"}, {"GET", "/v1/devices/count"}, {"DELETE", "/v1/devices"},
		{"POST", "/v1/campaigns"}, {"GET", "/v1/campaigns"}, {"GET", "/v1/campaigns/cmp_x"},
		{"GET", "/v1/campaigns/cmp_x/ticks"}, {"POST", "/v1/campaigns/cmp_x/start"},
	} {
		rec := f.do(tc.method, tc.path, "{}", false)
		if rec.Code != 401 || errorCode(t, rec) != "unauthorized" {
			t.Errorf("%s %s without token: status %d code %q, want 401 unauthorized", tc.method, tc.path, rec.Code, errorCode(t, rec))
		}
	}
}

func TestDeviceWalkthrough(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/devices/batch", `{"devices":[{"token":"dev-000001","platform":"android","app_build":120}]}`, true)
	t.Logf("batch: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"upserted":1}` {
		t.Fatalf("batch: status %d body %q", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", "/v1/devices/count", "", true)
	if strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("count body = %q", rec.Body.String())
	}
	rec = f.do("DELETE", "/v1/devices", "", true)
	if strings.TrimSpace(rec.Body.String()) != `{"deleted":1}` {
		t.Fatalf("delete body = %q", rec.Body.String())
	}
	if rec = f.do("GET", "/v1/devices/count", "", true); strings.TrimSpace(rec.Body.String()) != `{"count":0}` {
		t.Fatalf("count after delete = %q", rec.Body.String())
	}
}

func TestDeviceValidation(t *testing.T) {
	f := newFixture(t)
	tooMany := `{"devices":[` + strings.Repeat(`{"token":"t","platform":"ios","app_build":1},`, 5000) + `{"token":"t","platform":"ios","app_build":1}]}`
	tests := []struct{ name, body string }{
		{"empty body", ``},
		{"no devices", `{"devices":[]}`},
		{"too many devices", tooMany},
		{"empty token", `{"devices":[{"token":"","platform":"ios","app_build":1}]}`},
		{"long token", fmt.Sprintf(`{"devices":[{"token":"%s","platform":"ios","app_build":1}]}`, strings.Repeat("x", 201))},
		{"bad platform", `{"devices":[{"token":"a","platform":"palm","app_build":1}]}`},
		{"unknown field", `{"devices":[{"token":"a","platform":"ios","app_build":1,"extra":1}]}`},
		{"malformed", `{"devices":`},
	}
	for _, tc := range tests {
		rec := f.do("POST", "/v1/devices/batch", tc.body, true)
		t.Logf("%s: %d", tc.name, rec.Code)
		if rec.Code != 400 || errorCode(t, rec) != "bad_request" {
			t.Errorf("%s: status %d code %q, want 400 bad_request", tc.name, rec.Code, errorCode(t, rec))
		}
	}
	if n, _ := f.store.CountDevices(context.Background()); n != 0 {
		t.Errorf("rejected requests stored %d devices", n)
	}
}

func TestCreateCampaignAppliesDefaults(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/campaigns", createBody, true)
	v := decode[campaign.View](t, rec)
	t.Logf("create: %d status %s min %d max %d deadline %d id %s", rec.Code, v.Status, v.MinRate, v.MaxRate, v.DeadlineSeconds, v.ID)
	if rec.Code != 201 || v.Status != campaign.StatusDraft || v.MinRate != 20 || v.MaxRate != 5000 || v.DeadlineSeconds != 300 {
		t.Fatalf("status %d view %+v", rec.Code, v)
	}
	if len(v.ID) != 16 || !strings.HasPrefix(v.ID, "cmp_") {
		t.Fatalf("id = %q, want cmp_ + 12 hex", v.ID)
	}
}

func TestCreateCampaignValidation(t *testing.T) {
	f := newFixture(t)
	tests := []struct{ name, body string }{
		{"empty name", `{"name":"","title":"Hi","audience":{"kind":"all"},"pacing":"adaptive"}`},
		{"bad pacing", `{"name":"a","title":"Hi","audience":{"kind":"all"},"pacing":"fast"}`},
		{"static without rate", `{"name":"a","title":"Hi","audience":{"kind":"all"},"pacing":"static"}`},
		{"missing audience", `{"name":"a","title":"Hi","pacing":"adaptive"}`},
		{"min above max", `{"name":"a","title":"Hi","audience":{"kind":"all"},"pacing":"adaptive","min_rate":900,"max_rate":100}`},
		{"unknown field", `{"name":"a","title":"Hi","audience":{"kind":"all"},"pacing":"adaptive","nope":1}`},
	}
	for _, tc := range tests {
		rec := f.do("POST", "/v1/campaigns", tc.body, true)
		t.Logf("%s: %d %s", tc.name, rec.Code, strings.TrimSpace(rec.Body.String()))
		if rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, rec.Code)
		}
	}
}

func TestGetAndListCampaigns(t *testing.T) {
	f := newFixture(t)
	first := decode[campaign.View](t, f.do("POST", "/v1/campaigns", createBody, true))
	second := decode[campaign.View](t, f.do("POST", "/v1/campaigns", createBody, true))

	rec := f.do("GET", "/v1/campaigns/"+first.ID, "", true)
	if got := decode[campaign.View](t, rec); rec.Code != 200 || got.ID != first.ID {
		t.Fatalf("get: status %d view %+v", rec.Code, got)
	}
	if rec := f.do("GET", "/v1/campaigns/cmp_missing", "", true); rec.Code != 404 || errorCode(t, rec) != "not_found" {
		t.Fatalf("unknown id: status %d code %q, want 404 not_found", rec.Code, errorCode(t, rec))
	}

	list := decode[struct{ Campaigns []campaign.View }](t, f.do("GET", "/v1/campaigns", "", true))
	if len(list.Campaigns) != 2 || list.Campaigns[0].ID != second.ID {
		t.Fatalf("list = %+v, want two campaigns, newest first", list.Campaigns)
	}
}

func TestListTicks(t *testing.T) {
	f := newFixture(t)
	v := decode[campaign.View](t, f.do("POST", "/v1/campaigns", createBody, true))

	rec := f.do("GET", "/v1/campaigns/"+v.ID+"/ticks", "", true)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ticks":[]}` {
		t.Fatalf("empty ticks: status %d body %q, want an empty array", rec.Code, rec.Body.String())
	}
	f.store.InsertTick(context.Background(), store.Tick{CampaignID: v.ID, At: time.Now(), Reason: "increase", Mode: "NORMAL"})
	ticks := decode[struct{ Ticks []store.Tick }](t, f.do("GET", "/v1/campaigns/"+v.ID+"/ticks", "", true))
	if len(ticks.Ticks) != 1 || ticks.Ticks[0].Reason != "increase" {
		t.Fatalf("ticks = %+v", ticks.Ticks)
	}
	if rec := f.do("GET", "/v1/campaigns/cmp_missing/ticks", "", true); rec.Code != 404 {
		t.Fatalf("ticks of unknown campaign: status %d, want 404", rec.Code)
	}
}

func TestLifecycleEndpointsNotImplementedYet(t *testing.T) {
	f := newFixture(t)
	for _, action := range []string{"start", "pause", "resume", "abort"} {
		rec := f.do("POST", "/v1/campaigns/cmp_x/"+action, "", true)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status %d, want 501", action, rec.Code)
		}
	}
}
