package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pancakes-harness/internal/vox"
)

func TestChatLanes(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []struct{ Content string } }
		json.NewDecoder(r.Body).Decode(&req)
		answer := "voice answer"
		if strings.Contains(req.Messages[0].Content, "criteria label") {
			answer = "c0"
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, answer)
	}))
	defer s.Close()
	env := map[string]string{"VOX_DECISION": "chat", "VOX_VOICE": "chat", "VOX_CHAT_URL": s.URL, "VOX_CHAT_MODEL": "test-model", "VOX_CHAT_SAMPLES": "1", "VOX_JEV": "unused", "VOX_CPG": "unused"}
	getenv := func(k string) string { return env[k] }
	d := buildDecision(getenv).(vox.ChatDecision)
	if d.Samples != 1 || d.Client.URL != s.URL || d.Name() != "chat:test-model" {
		t.Fatalf("decision = %+v", d)
	}
	if buildVoice(getenv, false) != nil {
		t.Fatal("voice enabled despite off flag")
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"decide", "-choices", "apple,brick,cloud", "fruit?"}, "selected"},
		{[]string{"say", "hello"}, "voice answer"},
		{[]string{"doctor", "-json"}, `"decision": "chat:test-model"`},
	} {
		var out, stderr bytes.Buffer
		if code := run(tc.args, strings.NewReader(""), &out, &stderr, getenv); code != 0 {
			t.Fatalf("code %d: %s", code, &stderr)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("output = %s", &out)
		}
	}
}

func TestLegacyLanes(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"VOX_JEV": "jev-bin", "VOX_JEV_PROVIDER": "provider", "VOX_CPG": "cpg-bin", "VOX_CPG_MODEL": "model"}[k]
	}
	d := buildDecision(getenv).(vox.JevDecision)
	v := buildVoice(getenv, true).(vox.CPGVoice)
	if d.Bin != "jev-bin" || d.Provider != "provider" || v.Bin != "cpg-bin" || v.Model != "model" {
		t.Fatalf("lanes = %+v %+v", d, v)
	}
	t.Setenv("PATH", t.TempDir())
	missing := func(string) string { return "" }
	if buildDecision(missing) != nil || buildVoice(missing, true) != nil {
		t.Fatal("missing binaries opted into chat")
	}
	var out, stderr bytes.Buffer
	run([]string{"doctor"}, strings.NewReader(""), &out, &stderr, missing)
	for _, want := range []string{"VOX_DECISION=chat", "VOX_VOICE=chat"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, &out)
		}
	}
}
