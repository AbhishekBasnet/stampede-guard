package herdsim

import (
	"sync"
	"sync/atomic"
	"time"
)

const timelineSeconds = 3600

const (
	cSent = iota
	cSentNaive
	cSentGuarded
	cOK
	cShed
	cServerErrors
	cTimeouts
	cNetErrors
	cClientErrors
	cRetries
	cSuppressed
	cBudgetExhausted
	cPushes
	cOpens
	cOrders
	cOrdersOK
	numCounters
)

type Second struct {
	T               int64 `json:"t"`
	Sent            int64 `json:"sent"`
	SentNaive       int64 `json:"sent_naive"`
	SentGuarded     int64 `json:"sent_guarded"`
	OK              int64 `json:"ok"`
	Shed            int64 `json:"shed"`
	ServerErrors    int64 `json:"server_errors"`
	Timeouts        int64 `json:"timeouts"`
	NetErrors       int64 `json:"net_errors"`
	ClientErrors    int64 `json:"client_errors"`
	Retries         int64 `json:"retries"`
	Suppressed      int64 `json:"suppressed"`
	BudgetExhausted int64 `json:"budget_exhausted"`
	Pushes          int64 `json:"pushes"`
	Opens           int64 `json:"opens"`
	Orders          int64 `json:"orders"`
	OrdersOK        int64 `json:"orders_ok"`
}

func makeSecond(t int64, v [numCounters]int64) Second {
	return Second{
		T: t, Sent: v[cSent], SentNaive: v[cSentNaive], SentGuarded: v[cSentGuarded], OK: v[cOK], Shed: v[cShed],
		ServerErrors: v[cServerErrors], Timeouts: v[cTimeouts], NetErrors: v[cNetErrors], ClientErrors: v[cClientErrors],
		Retries: v[cRetries], Suppressed: v[cSuppressed], BudgetExhausted: v[cBudgetExhausted], Pushes: v[cPushes],
		Opens: v[cOpens], Orders: v[cOrders], OrdersOK: v[cOrdersOK],
	}
}

type Stats struct {
	cur, total [numCounters]atomic.Int64

	mu       sync.Mutex
	timeline []Second
}

func (s *Stats) Add(i int) {
	s.cur[i].Add(1)
	s.total[i].Add(1)
}

func (s *Stats) Total(i int) int64 { return s.total[i].Load() }

func (s *Stats) Swap(now time.Time) {
	var v [numCounters]int64
	for i := range v {
		v[i] = s.cur[i].Swap(0)
	}
	s.mu.Lock()
	s.timeline = append(s.timeline, makeSecond(now.Unix(), v))
	if over := len(s.timeline) - timelineSeconds; over > 0 {
		s.timeline = append([]Second(nil), s.timeline[over:]...)
	}
	s.mu.Unlock()
}

func (s *Stats) Totals() Second {
	var v [numCounters]int64
	for i := range v {
		v[i] = s.total[i].Load()
	}
	return makeSecond(0, v)
}

func (s *Stats) Since(unix int64) []Second {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Second{}
	for _, sec := range s.timeline {
		if sec.T >= unix {
			out = append(out, sec)
		}
	}
	return out
}

func (s *Stats) Last(n int) []Second {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := max(0, len(s.timeline)-n)
	return append([]Second{}, s.timeline[start:]...)
}

func (s *Stats) Reset() {
	for i := range s.cur {
		s.cur[i].Store(0)
		s.total[i].Store(0)
	}
	s.mu.Lock()
	s.timeline = nil
	s.mu.Unlock()
}

func (s *Stats) run(done <-chan struct{}, now func() time.Time) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.Swap(now())
		}
	}
}
