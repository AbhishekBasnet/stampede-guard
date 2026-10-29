package retry

import "testing"

func TestBudgetSpendAndEarn(t *testing.T) {
	b := NewBudget(10, 0.1)
	for i := 1; i <= 10; i++ {
		if !b.TryRetry() {
			t.Fatalf("retry %d of the initial 10 was refused", i)
		}
	}
	if b.TryRetry() {
		t.Fatalf("11th retry allowed on an empty budget")
	}

	for i := 0; i < 9; i++ {
		b.OnRequest()
	}
	t.Logf("after 9 requests at ratio 0.1")
	if b.TryRetry() {
		t.Fatalf("retry allowed after only 0.9 tokens earned")
	}
	b.OnRequest()
	if !b.TryRetry() {
		t.Fatalf("retry refused after 10 requests earned a full token")
	}
	if b.TryRetry() {
		t.Fatalf("a second retry was allowed on one token")
	}
}

func TestBudgetZeroRatioNeverRefills(t *testing.T) {
	b := NewBudget(10, 0)
	for i := 0; i < 10; i++ {
		b.TryRetry()
	}
	for i := 0; i < 1000; i++ {
		b.OnRequest()
	}
	if b.TryRetry() {
		t.Fatalf("ratio 0 budget refilled")
	}
}

func TestBudgetCapsAtMax(t *testing.T) {
	b := NewBudget(2, 1)
	for i := 0; i < 100; i++ {
		b.OnRequest()
	}
	if !b.TryRetry() || !b.TryRetry() || b.TryRetry() {
		t.Fatalf("budget should hold exactly its 2-token maximum")
	}
}

func TestBudgetSetRatio(t *testing.T) {
	b := NewBudget(10, 0.1)
	for i := 0; i < 10; i++ {
		b.TryRetry()
	}
	b.SetRatio(0.5)
	b.OnRequest()
	b.OnRequest()
	if !b.TryRetry() {
		t.Fatalf("two requests at ratio 0.5 should earn one token")
	}
}
