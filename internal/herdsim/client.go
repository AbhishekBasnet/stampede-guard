package herdsim

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
	"github.com/AbhishekBasnet/stampede-guard/pkg/retry"
)

type RequestSpec struct {
	Method, Path string
	Priority     policy.Priority
	CampaignID   string
}

type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeShed
	OutcomeServerError
	OutcomeTimeout
	OutcomeNetError
	OutcomeClientError
	OutcomeBudgetExhausted
)

var outcomeNames = [...]string{"ok", "shed", "server_error", "timeout", "net_error", "client_error", "budget_exhausted"}

func (o Outcome) String() string { return outcomeNames[o] }

func (o Outcome) retryable() bool {
	switch o {
	case OutcomeShed, OutcomeServerError, OutcomeTimeout, OutcomeNetError:
		return true
	}
	return false
}

const (
	naiveMaxRetries   = 5
	guardedMaxRetries = 3
	naiveRetryDelay   = time.Second
)

func (s *Sim) Do(ctx context.Context, d *Device, spec RequestSpec) Outcome {
	naive := d.Naive()
	maxRetries := guardedMaxRetries
	if naive {
		maxRetries = naiveMaxRetries
	} else {
		d.budget.OnRequest()
	}
	for attempt := 0; ; attempt++ {
		out, retryAfter := s.attempt(ctx, d, spec, naive)
		if !out.retryable() || attempt >= maxRetries {
			return out
		}
		var delay time.Duration
		if naive {
			delay = naiveRetryDelay
		} else {
			if !d.budget.TryRetry() {
				s.stats.Add(cBudgetExhausted)
				return OutcomeBudgetExhausted
			}
			pol := d.Policy(s.clk.Now())
			delay = max(retry.FullJitter(attempt, pol.BackoffMin, pol.BackoffMax), retryAfter)
		}
		s.stats.Add(cRetries)
		s.met.retries.WithLabelValues(kindName(naive)).Inc()
		if err := s.sleep(ctx, delay); err != nil {
			return out
		}
	}
}

func kindName(naive bool) string {
	if naive {
		return "naive"
	}
	return "guarded"
}

func (s *Sim) attempt(ctx context.Context, d *Device, spec RequestSpec, naive bool) (Outcome, time.Duration) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return s.record(naive, spec, OutcomeNetError), 0
	}
	actx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, spec.Method, s.cfg.APIBaseURL+spec.Path, nil)
	if err != nil {
		return s.record(naive, spec, OutcomeNetError), 0
	}
	req.Header.Set(policy.HeaderClientKind, kindName(naive))
	if spec.CampaignID != "" {
		req.Header.Set(policy.HeaderCampaignID, spec.CampaignID)
	}
	if !naive {
		req.Header.Set(policy.HeaderRequestPriority, spec.Priority.String())
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return s.record(naive, spec, OutcomeTimeout), 0
		}
		return s.record(naive, spec, OutcomeNetError), 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if h := resp.Header.Get(policy.HeaderFleetPolicy); h != "" && !naive {
		d.UpdatePolicy(h, s.clk.Now())
	}
	var retryAfter time.Duration
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		retryAfter = time.Duration(secs) * time.Second
	}
	return s.record(naive, spec, classify(resp)), retryAfter
}

func classify(resp *http.Response) Outcome {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return OutcomeOK
	case resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(policy.HeaderGuardShed) == "1":
		return OutcomeShed
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return OutcomeServerError
	default:
		return OutcomeClientError
	}
}

func (s *Sim) record(naive bool, spec RequestSpec, out Outcome) Outcome {
	s.stats.Add(cSent)
	if naive {
		s.stats.Add(cSentNaive)
	} else {
		s.stats.Add(cSentGuarded)
	}
	switch out {
	case OutcomeOK:
		s.stats.Add(cOK)
	case OutcomeShed:
		s.stats.Add(cShed)
	case OutcomeServerError:
		s.stats.Add(cServerErrors)
	case OutcomeTimeout:
		s.stats.Add(cTimeouts)
	case OutcomeNetError:
		s.stats.Add(cNetErrors)
	case OutcomeClientError:
		s.stats.Add(cClientErrors)
	}
	s.met.requests.WithLabelValues(kindName(naive), spec.Priority.String(), out.String()).Inc()
	return out
}
