package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Env struct{ errs []error }

func (e *Env) fail(key string, err error) {
	e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
}

func (e *Env) String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (e *Env) Int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.fail(key, err)
		return def
	}
	return n
}

func (e *Env) Float(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.fail(key, err)
		return def
	}
	return f
}

func (e *Env) Bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(key, err)
		return def
	}
	return b
}

func (e *Env) Millis(key string, defMs int) time.Duration {
	return time.Duration(e.Int(key, defMs)) * time.Millisecond
}

func (e *Env) Seconds(key string, defS int) time.Duration {
	return time.Duration(e.Int(key, defS)) * time.Second
}

func (e *Env) Err() error { return errors.Join(e.errs...) }
