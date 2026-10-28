package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLoggerFieldsAndLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, "warn", "demo-api")

	log.Info("hidden")
	if buf.Len() != 0 {
		t.Fatalf("info line written at warn level: %s", buf.String())
	}

	log.Warn("shown")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	for _, k := range []string{"time", "level", "msg", "service"} {
		if _, ok := line[k]; !ok {
			t.Errorf("missing field %q in %s", k, buf.String())
		}
	}
	if line["service"] != "demo-api" {
		t.Errorf("service = %v", line["service"])
	}
}
