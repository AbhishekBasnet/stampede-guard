package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "not_found", "campaign not found")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	want := `{"error":{"code":"not_found","message":"campaign not found"}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestReadJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name    string
		input   string
		max     int64
		wantErr bool
		want    string
	}{
		{"valid", `{"name":"a"}`, 1024, false, "a"},
		{"empty body is allowed", ``, 1024, false, ""},
		{"unknown field", `{"name":"a","x":1}`, 1024, true, ""},
		{"too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, 10, true, ""},
		{"bad json", `{`, 1024, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.input))
			var b body
			err := ReadJSON(httptest.NewRecorder(), r, &b, tc.max)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && b.Name != tc.want {
				t.Fatalf("name = %q, want %q", b.Name, tc.want)
			}
		})
	}
}

func TestRequireBearer(t *testing.T) {
	h := RequireBearer("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"no scheme", "secret", http.StatusUnauthorized},
		{"correct", "Bearer secret", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestNewServerTimeouts(t *testing.T) {
	s := NewServer(":0", http.NewServeMux())
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatal("a timeout is not set")
	}
}
