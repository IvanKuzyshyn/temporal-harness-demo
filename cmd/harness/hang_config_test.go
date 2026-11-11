package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
)

func TestSessionInputCarriesHangTimeout(t *testing.T) {
	want := 2500 * time.Millisecond
	data, err := json.Marshal(sessionInput(config.Config{HangTimeout: want}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var got time.Duration
	if raw := fields["hangTimeout"]; raw != nil {
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
	}
	if got != want {
		t.Fatalf("workflow hang timeout = %v, want %v", got, want)
	}
}
