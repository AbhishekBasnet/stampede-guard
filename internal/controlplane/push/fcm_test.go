package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

var now = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

type captured struct {
	mu     sync.Mutex
	path   string
	method string
	auth   string
	ctype  string
	body   map[string]any
}

func fakeFCM(t *testing.T, status int, header http.Header) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.path, c.method, c.auth, c.ctype = r.URL.Path, r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"details"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func newFCM(t *testing.T, baseURL string) *FCM {
	t.Helper()
	f, err := NewFCM(context.Background(), FCMConfig{
		BaseURL: baseURL, ProjectID: "demo-project", Timeout: time.Second, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func message() Message {
	return Message{Token: "dev-000123", Title: "Kohli is batting!", Body: "Watch live now",
		CampaignID: "cmp_0123456789ab", ExpiresAt: now.Add(287 * time.Second)}
}

func nested(t *testing.T, m map[string]any, keys ...string) any {
	t.Helper()
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %T is not an object", keys, cur)
		}
		cur = next[k]
	}
	return cur
}

func TestRequestShape(t *testing.T) {
	srv, got := fakeFCM(t, 200, nil)
	res := newFCM(t, srv.URL).Send(context.Background(), message())
	if !res.OK {
		t.Fatalf("Send = %+v, want OK", res)
	}
	t.Logf("request: %s %s body %v", got.method, got.path, got.body)
	if got.method != "POST" || got.path != "/v1/projects/demo-project/messages:send" || got.ctype != "application/json" {
		t.Fatalf("method %s path %s content-type %s", got.method, got.path, got.ctype)
	}
	checks := map[string]any{
		"token":                        "dev-000123",
		"notification.title":           "Kohli is batting!",
		"notification.body":            "Watch live now",
		"data.campaign_id":             "cmp_0123456789ab",
		"android.ttl":                  "287s",
		"apns.headers.apns-expiration": strconv.FormatInt(now.Add(287*time.Second).Unix(), 10),
	}
	for path, want := range checks {
		keys := append([]string{"message"}, strings.Split(path, ".")...)
		if got := nested(t, got.body, keys...); got != want {
			t.Errorf("message.%s = %v, want %v", path, got, want)
		}
	}
	if exp := nested(t, got.body, "message", "apns", "headers", "apns-expiration").(string); strings.Trim(exp, "0123456789") != "" {
		t.Errorf("apns-expiration %q is not all digits", exp)
	}
}

func TestTTLHasOneSecondMinimum(t *testing.T) {
	srv, got := fakeFCM(t, 200, nil)
	m := message()
	m.ExpiresAt = now.Add(-time.Minute)
	newFCM(t, srv.URL).Send(context.Background(), m)
	if ttl := nested(t, got.body, "message", "android", "ttl"); ttl != "1s" {
		t.Fatalf("ttl of an already expired push = %v, want 1s", ttl)
	}
}

func TestResponseMapping(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		header    http.Header
		wantOK    bool
		wantPerm  bool
		wantRetry time.Duration
		errPrefix string
	}{
		{"200", 200, nil, true, false, 0, ""},
		{"404 unregistered", 404, nil, false, true, 0, "http 404: "},
		{"400 invalid argument", 400, nil, false, true, 0, "http 400: "},
		{"403 sender mismatch", 403, nil, false, true, 0, "http 403: "},
		{"503 with Retry-After", 503, http.Header{"Retry-After": {"2"}}, false, false, 2 * time.Second, "http 503: "},
		{"429 with Retry-After", 429, http.Header{"Retry-After": {"7"}}, false, false, 7 * time.Second, "http 429: "},
		{"500", 500, nil, false, false, 0, "http 500: "},
		{"502", 502, nil, false, false, 0, "http 502: "},
		{"504", 504, nil, false, false, 0, "http 504: "},
		{"503 bad Retry-After", 503, http.Header{"Retry-After": {"soon"}}, false, false, 0, "http 503: "},
		{"401 is not permanent", 401, nil, false, false, 0, "http 401: "},
		{"418 other", 418, nil, false, false, 0, "http 418: "},
	}
	for _, tc := range tests {
		srv, _ := fakeFCM(t, tc.status, tc.header)
		res := newFCM(t, srv.URL).Send(context.Background(), message())
		t.Logf("%s -> %+v", tc.name, res)
		if res.OK != tc.wantOK || res.Permanent != tc.wantPerm || res.RetryAfter != tc.wantRetry {
			t.Errorf("%s: got %+v, want ok=%v permanent=%v retryAfter=%v", tc.name, res, tc.wantOK, tc.wantPerm, tc.wantRetry)
		}
		if !strings.HasPrefix(res.Err, tc.errPrefix) {
			t.Errorf("%s: Err = %q, want prefix %q", tc.name, res.Err, tc.errPrefix)
		}
	}
}

func TestErrorBodyIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer srv.Close()
	res := newFCM(t, srv.URL).Send(context.Background(), message())
	if want := len("http 500: ") + 200; len(res.Err) != want {
		t.Fatalf("len(Err) = %d, want %d", len(res.Err), want)
	}
}

func TestConnectionRefusedIsTransient(t *testing.T) {
	srv, _ := fakeFCM(t, 200, nil)
	url := srv.URL
	srv.Close()
	res := newFCM(t, url).Send(context.Background(), message())
	t.Logf("connection refused -> %+v", res)
	if res.OK || res.Permanent || res.Err == "" {
		t.Fatalf("got %+v, want a transient failure with an error text", res)
	}
}

func TestTimeoutIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	f, err := NewFCM(context.Background(), FCMConfig{BaseURL: srv.URL, ProjectID: "p", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	res := f.Send(context.Background(), message())
	if res.OK || res.Permanent || res.Err == "" {
		t.Fatalf("got %+v, want a transient failure", res)
	}
}

func TestNoAuthorizationWithoutCredentials(t *testing.T) {
	srv, got := fakeFCM(t, 200, nil)
	newFCM(t, srv.URL).Send(context.Background(), message())
	if got.auth != "" {
		t.Fatalf("Authorization = %q, want none for the fake FCM", got.auth)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	srv, got := fakeFCM(t, 200, nil)
	newFCM(t, srv.URL+"/").Send(context.Background(), message())
	if got.path != "/v1/projects/demo-project/messages:send" {
		t.Fatalf("path = %q", got.path)
	}
}

func TestCredentialsFileErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing file": filepath.Join(dir, "none.json"), "invalid credentials": bad} {
		_, err := NewFCM(context.Background(), FCMConfig{BaseURL: "http://x", ProjectID: "p", CredentialsFile: path, Timeout: time.Second})
		t.Logf("%s: %v", name, err)
		if err == nil {
			t.Errorf("%s: NewFCM succeeded", name)
		}
	}
}
