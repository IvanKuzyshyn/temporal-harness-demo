package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEveryEventRoundTrips(t *testing.T) {
	for _, typ := range Types {
		t.Run(string(typ), func(t *testing.T) {
			want := Event{Seq: 42, TS: "2026-11-09T00:00:00Z", WorkflowID: "session-test", RunID: "run", Type: typ, Pattern: None, Data: json.RawMessage(`{"test":"value"}`)}
			b, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got Event
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
	if len(Types) != 19 {
		t.Fatalf("event inventory: %d", len(Types))
	}
}
