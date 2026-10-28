package config

import (
	"testing"
	"time"
)

func TestEnvDefaultsAndValues(t *testing.T) {
	t.Setenv("T_EMPTY", "")
	t.Setenv("T_STR", "hello")
	t.Setenv("T_INT", "42")
	t.Setenv("T_FLOAT", "0.25")
	t.Setenv("T_BOOL", "true")
	t.Setenv("T_MS", "250")
	t.Setenv("T_S", "10")

	var e Env
	if got := e.String("T_EMPTY", "def"); got != "def" {
		t.Errorf("String empty = %q", got)
	}
	if got := e.String("T_STR", "def"); got != "hello" {
		t.Errorf("String = %q", got)
	}
	if got := e.Int("T_INT", 1); got != 42 {
		t.Errorf("Int = %d", got)
	}
	if got := e.Float("T_FLOAT", 1); got != 0.25 {
		t.Errorf("Float = %v", got)
	}
	if got := e.Bool("T_BOOL", false); !got {
		t.Errorf("Bool = %v", got)
	}
	if got := e.Millis("T_MS", 1); got != 250*time.Millisecond {
		t.Errorf("Millis = %v", got)
	}
	if got := e.Seconds("T_S", 1); got != 10*time.Second {
		t.Errorf("Seconds = %v", got)
	}
	if got := e.Int("T_MISSING", 7); got != 7 {
		t.Errorf("Int default = %d", got)
	}
	if err := e.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnvCollectsErrors(t *testing.T) {
	t.Setenv("T_BAD_INT", "abc")
	t.Setenv("T_BAD_BOOL", "maybe")

	var e Env
	if got := e.Int("T_BAD_INT", 5); got != 5 {
		t.Errorf("Int fallback = %d", got)
	}
	e.Bool("T_BAD_BOOL", false)
	if e.Err() == nil {
		t.Fatal("want an error for invalid values")
	}
}
