package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestParseLineCommands(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want parsedLine
	}{
		{in: "hello", want: parsedLine{kind: "text", arg: "hello"}},
		{in: ":agent summarize", want: parsedLine{kind: "agent", arg: "summarize"}},
		{in: ":fork alt", want: parsedLine{kind: "fork", arg: "alt"}},
		{in: ":replay", want: parsedLine{kind: "replay", arg: ""}},
		{in: ":status", want: parsedLine{kind: "status", arg: ""}},
		{in: ":mode agent", want: parsedLine{kind: "mode", arg: "agent"}},
		{in: ":quit", want: parsedLine{kind: "quit", arg: ""}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := parseLine(tc.in); got != tc.want {
				t.Fatalf("parseLine(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildTurnAndAgentRequest(t *testing.T) {
	t.Parallel()

	turn := buildTurnRequest(" s1 ", " main ", " hello ")
	wantTurn := map[string]any{
		"session_id": "s1",
		"branch_id":  "main",
		"text":       "hello",
	}
	if !reflect.DeepEqual(turn, wantTurn) {
		t.Fatalf("turn request mismatch: got %#v want %#v", turn, wantTurn)
	}

	agent := buildAgentCallRequest(" s2 ", " b1 ", " summarize ")
	wantAgent := map[string]any{
		"session_id":  "s2",
		"branch_id":   "b1",
		"task":        "summarize",
		"allow_tools": false,
	}
	if !reflect.DeepEqual(agent, wantAgent) {
		t.Fatalf("agent request mismatch: got %#v want %#v", agent, wantAgent)
	}
}

func TestReplModeSwitchRoutesTextToAgentCall(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calls := map[string]int{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agent-call":
			io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"session_id":"s1","branch_id":"main","decision":"answer","resolved":true,"fingerprint":"abc","answer":"ok"}`))
		case "/v1/turn":
			io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"session_id":"s1","branch_id":"main","decision":"answer","answer":"ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
		}
	}))
	defer ts.Close()

	input := bytes.NewBufferString(":mode agent\nhello from repl\n:quit\n")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	exit := run([]string{"--addr", ts.URL, "--session-id", "s1", "--branch-id", "main"}, input, out, errOut)
	if exit != 0 {
		t.Fatalf("run exit=%d stderr=%s", exit, errOut.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["/v1/agent-call"] != 1 {
		t.Fatalf("expected one /v1/agent-call request, got %#v", calls)
	}
	if calls["/v1/turn"] != 0 {
		t.Fatalf("expected zero /v1/turn requests after mode switch, got %#v", calls)
	}
}

func TestHandleTurnAndAgentRequestConstruction(t *testing.T) {
	t.Parallel()

	type captured struct {
		Path string
		Body map[string]any
	}
	got := make(chan captured, 2)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		got <- captured{Path: r.URL.Path, Body: body}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/turn" {
			_, _ = w.Write([]byte(`{"session_id":"s1","branch_id":"main","decision":"answer","answer":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"s1","branch_id":"main","decision":"answer","resolved":true,"fingerprint":"abc","answer":"ok"}`))
	}))
	defer ts.Close()

	c := &cli{
		cfg:    config{addr: ts.URL, sessionID: "s1", branchID: "main"},
		client: ts.Client(),
		out:    io.Discard,
		err:    io.Discard,
	}
	if err := c.handleTurn("hello"); err != nil {
		t.Fatalf("handleTurn: %v", err)
	}
	if err := c.handleAgent("summarize"); err != nil {
		t.Fatalf("handleAgent: %v", err)
	}

	first := <-got
	second := <-got
	all := []captured{first, second}

	var turnSeen, agentSeen bool
	for _, v := range all {
		switch v.Path {
		case "/v1/turn":
			turnSeen = true
			if v.Body["session_id"] != "s1" || v.Body["branch_id"] != "main" || v.Body["text"] != "hello" {
				t.Fatalf("unexpected turn body: %#v", v.Body)
			}
		case "/v1/agent-call":
			agentSeen = true
			if v.Body["session_id"] != "s1" || v.Body["branch_id"] != "main" || v.Body["task"] != "summarize" || v.Body["allow_tools"] != false {
				t.Fatalf("unexpected agent body: %#v", v.Body)
			}
		}
	}
	if !turnSeen || !agentSeen {
		t.Fatalf("expected both /v1/turn and /v1/agent-call, got %#v", all)
	}
}

func TestHandleAgentCompactOutputResolved(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"session_id":"s1",
			"branch_id":"main",
			"decision":"answer",
			"resolved":true,
			"fingerprint":"1234567890abcdef",
			"answer":"done",
			"trace":{"consult_manifest":{"actual_bytes":640,"byte_budget":14336}}
		}`))
	}))
	defer ts.Close()

	out := &bytes.Buffer{}
	c := &cli{
		cfg:    config{addr: ts.URL, sessionID: "s1", branchID: "main"},
		client: ts.Client(),
		out:    out,
		err:    io.Discard,
	}
	if err := c.handleAgent("summarize"); err != nil {
		t.Fatalf("handleAgent: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"agent resolved",
		"fp=1234567890ab",
		"consult=yes",
		"bytes=640/14336",
		"answer=done",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
}

func TestHandleAgentCompactOutputUnresolved(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"session_id":"s1",
			"branch_id":"main",
			"decision":"unresolved",
			"resolved":false,
			"missing":["scope"]
		}`))
	}))
	defer ts.Close()

	out := &bytes.Buffer{}
	c := &cli{
		cfg:    config{addr: ts.URL, sessionID: "s1", branchID: "main"},
		client: ts.Client(),
		out:    out,
		err:    io.Discard,
	}
	if err := c.handleAgent("summarize"); err != nil {
		t.Fatalf("handleAgent: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"agent unresolved",
		"fp=-",
		"consult=no",
		"missing=scope",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
}
