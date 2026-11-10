package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJournalOutcomeAmbiguities(t *testing.T) {
	ordinary := `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","executionError":"false","logRef":"logs/x","truncated":"false"}}`
	verification := `{"tail":"CAPTURE","logRef":"logs/x","exitCode":0,"isError":false,"executionError":false,"interrupted":false,"truncated":false}`
	cases := map[string]string{
		// Verbatim R2 rereview reproductions.
		"rereview identity": `{"toolCallId":"other","ToolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0"}}`,
		"rereview failure":  `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","executionError":"true"},"Details":{"executionError":"false"}}`,
		"rereview exit":     `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"invalid"},"Details":{"exitCode":"0"}}`,
	}
	for schema, base := range map[string]string{"ordinary": ordinary, "verification": verification} {
		// Aliases must be refused even without a canonical counterpart, and even
		// when they happen to agree. Neither schema can supply alternate evidence.
		for _, key := range []string{"toolCallId", "output", "structured", "details", "isError", "interrupted", "exitCode", "executionError", "tail", "logRef", "truncated"} {
			cases[schema+" alias "+key] = `{"` + strings.ToUpper(key) + `":null,` + base[1:]
		}
		for _, change := range []struct{ key, old, bad string }{
			{"isError", `false`, `true`},
			{"exitCode", `0`, `2`},
			{"executionError", `false`, `true`},
		} {
			old, bad := change.old, change.bad
			if schema == "ordinary" && change.key != "isError" {
				old, bad = `"`+old+`"`, `"`+bad+`"`
			}
			member := `"` + change.key + `":`
			for _, pair := range []string{bad + `,` + member + old, old + `,` + member + bad} {
				cases[schema+" duplicate "+change.key+pair] = strings.Replace(base, member+old, member+pair, 1)
			}
		}
		for _, key := range []string{"toolCallId", "output", "isError", "interrupted", "details", "tail", "logRef", "exitCode", "executionError", "truncated"} {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(base), &fields); err != nil {
				t.Fatal(err)
			}
			old, present := fields[key]
			if !present {
				continue
			}
			for _, bad := range []string{" \n null \t ", `[]`, `{}`} {
				cases[schema+" malformed "+key+bad] = strings.Replace(base, `"`+key+`":`+string(old), `"`+key+`":`+bad, 1)
			}
		}
	}
	for _, pair := range []string{`"other","toolCallId":"call"`, `"call","toolCallId":"other"`, `null,"toolCallId":"call"`} {
		cases["duplicate identity "+pair] = strings.Replace(ordinary, `"toolCallId":"call"`, `"toolCallId":`+pair, 1)
	}
	cases["escaped duplicate identity"] = `{"toolCall\u0049d":"other",` + ordinary[1:]
	cases["duplicate details"] = strings.TrimSuffix(ordinary, "}") + `,"details":{"exitCode":"0"}}`
	cases["duplicate output preserves both"] = `{"output":"FIRST",` + ordinary[1:]
	for _, member := range []string{
		`"toolCallId":"other","toolCallId":"call",`,
		`"isError":"true","isError":"false",`,
		`"executionError":null,"executionError":"false",`,
		`"exitCode":"invalid","exitCode":"0",`,
	} {
		cases["duplicate detail "+member] = strings.Replace(ordinary, `"details":{`, `"details":{`+member, 1)
	}
	cases["escaped detail alias"] = strings.Replace(ordinary, `"details":{`, `"details":{"\u0045xitCode":"0",`, 1)
	for _, key := range []string{"exitCode", "executionError", "logRef", "truncated"} {
		cases["detail alias "+key] = strings.Replace(ordinary, `"details":{`, `"details":{"`+strings.ToUpper(key)+`":"false",`, 1)
	}
	for _, bad := range []string{`null`, `true`, `0`, `[]`, `{}`} {
		cases["malformed detail "+bad] = strings.Replace(ordinary, `"executionError":"false"`, `"executionError": `+bad+` `, 1)
	}
	cases["null optional interruption"] = `{"interrupted": null ,` + ordinary[1:]
	cases["mixed verification output"] = `{"output":"OTHER",` + verification[1:]
	cases["mixed verification details"] = `{"details":{"exitCode":"2"},` + verification[1:]
	cases["alias identity alone"] = strings.Replace(ordinary, "toolCallId", "ToolCallId", 1)
	cases["alias detail alone"] = strings.Replace(ordinary, "executionError", "ExecutionError", 1)
	cases["trailing object"] = ordinary + `{}`
	cases["trailing junk"] = ordinary + `!`
	cases["truncated object"] = strings.TrimSuffix(ordinary, "}")
	cases["unknown extension cannot supply exit"] = `{"toolCallId":"call","output":"CAPTURE","isError":false,"extension":{"exitCode":0}}`
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			got := durableToolText("run_command", "call", content)
			if !strings.HasPrefix(got, "[outcome unavailable") {
				t.Fatalf("ambiguous evidence certified: %s", got)
			}
			// Refused certification must retain all original evidence, including
			// duplicate values, null/malformed output and differently cased fields.
			if !strings.Contains(got, content) {
				t.Fatalf("lost raw evidence:\nwant %s\ngot %s", content, got)
			}
		})
	}
	for name, base := range map[string]string{"ordinary": ordinary, "verification": verification} {
		t.Run(name+" unrelated extension", func(t *testing.T) {
			content := `{"extension":{"ExitCode":"invalid"},` + base[1:]
			got := durableToolText("run_command", "call", content)
			if !strings.HasPrefix(got, "[succeeded]") || !strings.Contains(got, "extension") || !strings.Contains(got, "CAPTURE") || !strings.Contains(got, "logs/x") {
				t.Fatal(got)
			}
		})
	}
}

func TestJournalStrictFieldNull(t *testing.T) {
	for _, raw := range []string{"null", " \n null \t "} {
		var flag bool
		var code int
		var text string
		for _, into := range []any{&flag, &code, &text} {
			if strictField(json.RawMessage(raw), into) {
				t.Fatalf("accepted null scalar %q into %T", raw, into)
			}
		}
	}
}
