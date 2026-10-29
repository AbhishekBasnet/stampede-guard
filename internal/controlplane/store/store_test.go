package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
)

var t0 = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "nested", "cp.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newCampaign(id string) *campaign.Campaign {
	r := campaign.CreateRequest{
		Name: "test", Title: "Hi", Body: "Hello",
		Audience: campaign.Audience{Kind: campaign.AudienceSample, Fraction: 0.25},
		Pacing:   campaign.PacingAdaptive, AmplificationPrior: 1.5,
	}
	r.ApplyDefaults(20, 5000)
	return campaign.New(r, id, t0)
}

func tokens(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("dev-%06d", i)
	}
	return out
}

func devices(n int) []Device {
	out := make([]Device, n)
	for i, tok := range tokens(n) {
		out[i] = Device{Token: tok, Platform: "android", AppBuild: 120}
	}
	return out
}

func startedCampaign(t *testing.T, s *Store, id string, n int) context.Context {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateCampaign(ctx, newCampaign(id)); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if err := s.MarkStarted(ctx, id, tokens(n), t0, t0.Add(5*time.Minute), 42); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	return ctx
}

func TestUpsertDevicesIsIdempotent(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	n, err := s.UpsertDevices(ctx, devices(2500), t0)
	if err != nil || n != 2500 {
		t.Fatalf("UpsertDevices = %d, %v, want 2500", n, err)
	}
	if _, err := s.UpsertDevices(ctx, devices(2500), t0); err != nil {
		t.Fatal(err)
	}
	count, _ := s.CountDevices(ctx)
	t.Logf("devices after two identical upserts: %d", count)
	if count != 2500 {
		t.Fatalf("count = %d, want 2500", count)
	}
}

func TestUpsertDevicesUpdatesBuild(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	s.UpsertDevices(ctx, []Device{{Token: "a", Platform: "ios", AppBuild: 1}}, t0)
	s.UpsertDevices(ctx, []Device{{Token: "a", Platform: "android", AppBuild: 2}}, t0)
	var platform string
	var build int
	if err := s.db.QueryRow(`SELECT platform, app_build FROM devices WHERE token = 'a'`).Scan(&platform, &build); err != nil {
		t.Fatal(err)
	}
	if platform != "android" || build != 2 {
		t.Fatalf("device = %s/%d, want android/2", platform, build)
	}
}

func TestRejectsUnknownPlatform(t *testing.T) {
	s := openTemp(t)
	if _, err := s.UpsertDevices(context.Background(), []Device{{Token: "a", Platform: "palm", AppBuild: 1}}, t0); err == nil {
		t.Fatalf("a device with an unknown platform was stored")
	}
}

func TestAllTokensAndDelete(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	s.UpsertDevices(ctx, []Device{{"b", "ios", 1}, {"a", "ios", 1}, {"c", "android", 1}}, t0)
	got, err := s.AllDeviceTokens(ctx)
	if err != nil || fmt.Sprint(got) != "[a b c]" {
		t.Fatalf("AllDeviceTokens = %v, %v, want [a b c]", got, err)
	}
	deleted, _ := s.DeleteAllDevices(ctx)
	if count, _ := s.CountDevices(ctx); deleted != 3 || count != 0 {
		t.Fatalf("deleted %d, left %d, want 3 and 0", deleted, count)
	}
}

func TestCampaignRoundTrip(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	want := newCampaign("cmp_0123456789ab")
	if err := s.CreateCampaign(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCampaign(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("round trip: %+v", got)
	if got.ID != want.ID || got.Name != want.Name || got.Title != want.Title || got.Body != want.Body ||
		got.Audience != want.Audience || got.Pacing != want.Pacing || got.MinRate != 20 || got.MaxRate != 5000 ||
		got.DeadlineSeconds != 300 || got.AmplificationPrior != 1.5 || got.Status != campaign.StatusDraft ||
		!got.CreatedAt.Equal(want.CreatedAt) || got.StartedAt != nil || got.DeadlineAt != nil || got.FinishedAt != nil {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if _, err := s.GetCampaign(ctx, "cmp_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestListCampaigns(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	for i, id := range []string{"cmp_000000000001", "cmp_000000000002", "cmp_000000000003"} {
		c := newCampaign(id)
		c.CreatedAt = t0.Add(time.Duration(i) * time.Second)
		s.CreateCampaign(ctx, c)
	}
	s.SetStatus(ctx, "cmp_000000000002", campaign.StatusAborted, &t0)

	all, _ := s.ListCampaigns(ctx, 2)
	if len(all) != 2 || all[0].ID != "cmp_000000000003" || all[1].ID != "cmp_000000000002" {
		t.Fatalf("ListCampaigns(2) not newest first: %v", all)
	}
	aborted, _ := s.ListCampaignsByStatus(ctx, campaign.StatusAborted)
	if len(aborted) != 1 || aborted[0].ID != "cmp_000000000002" || aborted[0].FinishedAt == nil {
		t.Fatalf("ListCampaignsByStatus(aborted) = %v", aborted)
	}
}

func TestSetStatusKeepsFinishedAtWhenNil(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	s.CreateCampaign(ctx, newCampaign("cmp_0123456789ab"))
	s.SetStatus(ctx, "cmp_0123456789ab", campaign.StatusCompleted, &t0)
	s.SetStatus(ctx, "cmp_0123456789ab", campaign.StatusAborted, nil)
	c, _ := s.GetCampaign(ctx, "cmp_0123456789ab")
	if c.FinishedAt == nil || !c.FinishedAt.Equal(t0) {
		t.Fatalf("finished_at = %v, want %v", c.FinishedAt, t0)
	}
	if err := s.SetStatus(ctx, "cmp_missing", campaign.StatusAborted, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus on unknown id: %v, want ErrNotFound", err)
	}
}

func TestMarkStartedIsIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := startedCampaign(t, s, "cmp_0123456789ab", 100)
	c, _ := s.GetCampaign(ctx, "cmp_0123456789ab")
	pending, _ := s.CountPending(ctx, c.ID)
	t.Logf("after start: targeted %d pending %d status %s", c.Targeted, pending, c.Status)
	if c.Targeted != 100 || pending != 100 || c.Status != campaign.StatusRunning || c.BaselineRPS != 42 {
		t.Fatalf("targeted %d pending %d status %s baseline %v", c.Targeted, pending, c.Status, c.BaselineRPS)
	}
	if c.StartedAt == nil || !c.StartedAt.Equal(t0) || c.DeadlineAt == nil || !c.DeadlineAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("started_at %v deadline_at %v", c.StartedAt, c.DeadlineAt)
	}

	if err := s.MarkStarted(ctx, c.ID, tokens(100), t0, t0.Add(5*time.Minute), 42); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetCampaign(ctx, c.ID)
	if pending, _ = s.CountPending(ctx, c.ID); c.Targeted != 100 || pending != 100 {
		t.Fatalf("second MarkStarted duplicated rows: targeted %d pending %d", c.Targeted, pending)
	}
}

func TestMarkStartedUnknownCampaign(t *testing.T) {
	s := openTemp(t)
	err := s.MarkStarted(context.Background(), "cmp_missing", tokens(3), t0, t0, 0)
	if err == nil {
		t.Fatalf("MarkStarted on an unknown campaign succeeded")
	}
	if n, _ := s.CountPending(context.Background(), "cmp_missing"); n != 0 {
		t.Fatalf("rows left behind by a failed start: %d", n)
	}
}

func TestApplyResultsAndRetries(t *testing.T) {
	s := openTemp(t)
	ctx := startedCampaign(t, s, "cmp_0123456789ab", 100)
	const id = "cmp_0123456789ab"

	batch, err := s.FetchPending(ctx, id, 30)
	if err != nil || len(batch) != 30 {
		t.Fatalf("FetchPending = %d tokens, %v, want 30", len(batch), err)
	}
	var results []DeliveryResult
	for i, tok := range batch {
		switch {
		case i < 20:
			results = append(results, DeliveryResult{Token: tok, OK: true})
		case i < 25:
			results = append(results, DeliveryResult{Token: tok, Permanent: true, Err: "http 404"})
		default:
			results = append(results, DeliveryResult{Token: tok, Err: "http 503"})
		}
	}
	sent, failed, err := s.ApplyResults(ctx, id, results, 3, t0)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCampaign(ctx, id)
	pending, _ := s.CountPending(ctx, id)
	t.Logf("first apply: sent %d failed %d; campaign sent %d failed %d pending %d", sent, failed, c.Sent, c.Failed, pending)
	if sent != 20 || failed != 5 || c.Sent != 20 || c.Failed != 5 || pending != 75 {
		t.Fatalf("sent %d failed %d campaign %d/%d pending %d, want 20 5 20/5 75", sent, failed, c.Sent, c.Failed, pending)
	}

	transient := results[25:]
	for round := 2; round <= 3; round++ {
		if _, _, err := s.ApplyResults(ctx, id, transient, 3, t0); err != nil {
			t.Fatal(err)
		}
	}
	c, _ = s.GetCampaign(ctx, id)
	pending, _ = s.CountPending(ctx, id)
	t.Logf("after attempts 2 and 3: failed %d pending %d", c.Failed, pending)
	if c.Failed != 10 || pending != 70 {
		t.Fatalf("failed %d pending %d, want 10 70", c.Failed, pending)
	}
}

func TestApplyResultsIgnoresDuplicates(t *testing.T) {
	s := openTemp(t)
	ctx := startedCampaign(t, s, "cmp_0123456789ab", 5)
	const id = "cmp_0123456789ab"
	batch, _ := s.FetchPending(ctx, id, 2)
	results := []DeliveryResult{{Token: batch[0], OK: true}, {Token: batch[1], OK: true}}
	s.ApplyResults(ctx, id, results, 3, t0)
	sent, _, _ := s.ApplyResults(ctx, id, results, 3, t0)
	c, _ := s.GetCampaign(ctx, id)
	if sent != 0 || c.Sent != 2 {
		t.Fatalf("duplicate results counted again: delta %d campaign sent %d, want 0 and 2", sent, c.Sent)
	}
}

func TestFetchPendingSkipsFinishedRows(t *testing.T) {
	s := openTemp(t)
	ctx := startedCampaign(t, s, "cmp_0123456789ab", 10)
	first, _ := s.FetchPending(ctx, "cmp_0123456789ab", 3)
	s.ApplyResults(ctx, "cmp_0123456789ab", []DeliveryResult{{Token: first[0], OK: true}}, 3, t0)
	next, _ := s.FetchPending(ctx, "cmp_0123456789ab", 3)
	if next[0] != first[1] {
		t.Fatalf("FetchPending returned %v after %v was sent, want to start at %s", next, first[0], first[1])
	}
}

func TestExpirePending(t *testing.T) {
	s := openTemp(t)
	ctx := startedCampaign(t, s, "cmp_0123456789ab", 100)
	const id = "cmp_0123456789ab"
	batch, _ := s.FetchPending(ctx, id, 30)
	var results []DeliveryResult
	for _, tok := range batch {
		results = append(results, DeliveryResult{Token: tok, OK: true})
	}
	s.ApplyResults(ctx, id, results, 3, t0)

	n, err := s.ExpirePending(ctx, id, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCampaign(ctx, id)
	pending, _ := s.CountPending(ctx, id)
	t.Logf("expired %d; campaign expired %d status %s pending %d", n, c.Expired, c.Status, pending)
	if n != 70 || c.Expired != 70 || pending != 0 || c.Status != campaign.StatusExpired || c.FinishedAt == nil {
		t.Fatalf("expired %d/%d pending %d status %s finished %v", n, c.Expired, pending, c.Status, c.FinishedAt)
	}
}

func TestTicksInTimeOrder(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	for _, off := range []int{2, 0, 1} {
		err := s.InsertTick(ctx, Tick{
			CampaignID: "cmp_0123456789ab", At: t0.Add(time.Duration(off) * time.Second), Rate: float64(off),
			RateCap: 100, Reason: "increase", SentTotal: int64(off), Headroom: 0.9, Mode: "NORMAL", P99Ms: 30, Amplification: 1.5,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.InsertTick(ctx, Tick{CampaignID: "cmp_other", At: t0, Reason: "x", Mode: "NORMAL"})

	ticks, err := s.ListTicks(ctx, "cmp_0123456789ab")
	if err != nil || len(ticks) != 3 {
		t.Fatalf("ListTicks = %d rows, %v, want 3", len(ticks), err)
	}
	for i, tk := range ticks {
		if tk.Rate != float64(i) || tk.Reason != "increase" || tk.Mode != "NORMAL" || tk.RateCap != 100 || tk.Amplification != 1.5 {
			t.Errorf("tick %d = %+v", i, tk)
		}
	}
}

func TestLatestFinalAmplification(t *testing.T) {
	s, ctx := openTemp(t), context.Background()
	if _, ok, err := s.LatestFinalAmplification(ctx); ok || err != nil {
		t.Fatalf("empty store: ok %v err %v, want false nil", ok, err)
	}
	s.CreateCampaign(ctx, newCampaign("cmp_0123456789ab"))
	s.SetFinalAmplification(ctx, "cmp_0123456789ab", 1.9)
	if _, ok, _ := s.LatestFinalAmplification(ctx); ok {
		t.Fatalf("an unfinished campaign was used for learning")
	}
	s.SetStatus(ctx, "cmp_0123456789ab", campaign.StatusCompleted, &t0)
	v, ok, err := s.LatestFinalAmplification(ctx)
	t.Logf("latest final amplification: %v %v %v", v, ok, err)
	if !ok || err != nil || v != 1.9 {
		t.Fatalf("got %v %v %v, want 1.9 true nil", v, ok, err)
	}
	if err := s.SetFinalAmplification(ctx, "cmp_missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertDevices(ctx, devices(10), t0)
	s.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if n, _ := s.CountDevices(ctx); n != 10 {
		t.Fatalf("devices after reopen = %d, want 10", n)
	}
}
