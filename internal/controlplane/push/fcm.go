package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

const (
	fcmScope     = "https://www.googleapis.com/auth/firebase.messaging"
	maxErrorBody = 200
)

type FCMConfig struct {
	BaseURL         string
	ProjectID       string
	CredentialsFile string
	Timeout         time.Duration
	Clock           clock.Clock // nil means the real clock
}

type FCM struct {
	url    string
	client *http.Client
	clock  clock.Clock
}

func NewFCM(ctx context.Context, cfg FCMConfig) (*FCM, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	f := &FCM{
		url:    strings.TrimRight(cfg.BaseURL, "/") + "/v1/projects/" + cfg.ProjectID + "/messages:send",
		client: &http.Client{Timeout: cfg.Timeout},
		clock:  clk,
	}
	if cfg.CredentialsFile == "" {
		return f, nil
	}
	data, err := os.ReadFile(cfg.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read FCM credentials: %w", err)
	}
	creds, err := google.CredentialsFromJSONWithType(ctx, data, google.ServiceAccount, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("parse FCM credentials: %w", err)
	}
	// The oauth2 client takes its timeout from the context's HTTP client.
	authCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: cfg.Timeout})
	f.client = oauth2.NewClient(authCtx, creds.TokenSource)
	f.client.Timeout = cfg.Timeout
	return f, nil
}

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data"`
	Android      fcmAndroid        `json:"android"`
	APNS         fcmAPNS           `json:"apns"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type fcmAndroid struct {
	TTL string `json:"ttl"`
}

type fcmAPNS struct {
	Headers map[string]string `json:"headers"`
}

func (f *FCM) Send(ctx context.Context, m Message) Result {
	ttl := max(int64(m.ExpiresAt.Sub(f.clock.Now())/time.Second), 1)
	payload, err := json.Marshal(fcmRequest{Message: fcmMessage{
		Token:        m.Token,
		Notification: fcmNotification{Title: m.Title, Body: m.Body},
		Data:         map[string]string{"campaign_id": m.CampaignID},
		Android:      fcmAndroid{TTL: strconv.FormatInt(ttl, 10) + "s"},
		APNS:         fcmAPNS{Headers: map[string]string{"apns-expiration": strconv.FormatInt(m.ExpiresAt.Unix(), 10)}},
	}})
	if err != nil {
		return Result{Permanent: true, Err: fmt.Sprintf("encode message: %v", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(payload))
	if err != nil {
		return Result{Err: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return Result{Err: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return Result{OK: true}
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
		return Result{Permanent: true, Err: httpErr(resp.StatusCode, body)}
	default:
		return Result{RetryAfter: retryAfter(resp), Err: httpErr(resp.StatusCode, body)}
	}
}

func httpErr(status int, body []byte) string {
	return fmt.Sprintf("http %d: %s", status, body)
}

func retryAfter(resp *http.Response) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
