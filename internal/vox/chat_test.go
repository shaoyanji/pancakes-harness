package vox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fastChat(t *testing.T, handler http.HandlerFunc) (*ChatClient, *[]time.Duration) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	now := time.Unix(100, 0)
	var delays []time.Duration
	return &ChatClient{URL: s.URL, now: func() time.Time { return now }, sleep: func(ctx context.Context, d time.Duration) error {
		delays = append(delays, d)
		now = now.Add(d)
		return ctx.Err()
	}}, &delays
}

func TestChatClient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		calls   int
		wantErr string
	}{
		{"success", 200, `{"choices":[{"message":{"content":" hello "}}],"related_apis":[1]}`, 1, ""},
		{"provider error", 400, `{"error":{"message":"bad model","type":"invalid","code":"bad"}}`, 1, "bad model"},
		{"retry", 503, `{"error":{"message":"busy"}}`, 2, ""},
		{"daily", 429, `{"error":{"message":"Daily allowance used up"}}`, 1, "Daily allowance"},
		{"empty", 200, `{"choices":[]}`, 1, "no content"},
		{"malformed", 200, `{`, 1, "parse chat"},
		{"short rate limit", 429, `{"error":{"message":"slow down"}}`, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, delays := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Errorf("request: %s %s %v", r.Method, r.URL, r.Header)
				}
				if calls == 1 {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"hello"}}]}`)
				}
			})
			got, err := c.complete(context.Background(), "test")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || got != "hello" {
				t.Fatalf("got %q, %v", got, err)
			}
			if calls != tc.calls {
				t.Fatalf("calls = %d", calls)
			}
			if tc.calls == 2 && ((*delays)[0] != 2*time.Second) {
				t.Fatalf("delays = %v", *delays)
			}
		})
	}
}

func TestChatSpacingAndVoice(t *testing.T) {
	var prompts []string
	c, delays := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model       string
			Temperature float64
			Messages    []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "custom" || req.Temperature <= 0 || len(req.Messages) != 1 || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("request = %+v", req)
		}
		prompts = append(prompts, req.Messages[0].Content)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	})
	c.Key, c.Model = "test", "custom"
	v := ChatVoice{Client: c}
	prompt := ExplainPrompt("question", "tool", nil, "output")
	for i := 0; i < 2; i++ {
		got, err := v.Say(context.Background(), prompt)
		if err != nil || got != "answer" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if v.Name() != "chat:custom" || len(prompts) != 2 || prompts[0] != prompt || prompts[1] != prompt {
		t.Fatalf("prompts = %v", prompts)
	}
	if len(*delays) != 1 || (*delays)[0] != time.Second {
		t.Fatalf("delays = %v", *delays)
	}
}

func TestChatDecision(t *testing.T) {
	for _, tc := range []struct {
		name         string
		replies      []string
		pick, status string
		prob, margin float64
		wantErr      bool
	}{
		{"agree", []string{"a", "a", "a"}, "a", "selected", 1, 1, false},
		{"majority", []string{"b", "a", "b"}, "b", "selected", 2.0 / 3, 1.0 / 3, false},
		{"tie", []string{"a", "b"}, "a", "needs_review", .5, 0, false},
		{"invalid", []string{"a", "a", "apple"}, "a", "selected", 2.0 / 3, 2.0 / 3, false},
		{"empty vote", []string{"a", "a", ""}, "a", "selected", 2.0 / 3, 2.0 / 3, false},
		{"no majority", []string{"a", "no", "no"}, "a", "needs_review", 1.0 / 3, 1.0 / 3, false},
		{"none valid", []string{"apple", "a.", "unknown"}, "", "", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			c, _ := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Messages []struct{ Content string } }
				json.NewDecoder(r.Body).Decode(&req)
				for _, want := range []string{"context", "question", "instructions", "apple", "brick", "exactly one"} {
					if !strings.Contains(req.Messages[0].Content, want) {
						t.Errorf("missing %s", want)
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.replies[i]}}}})
				i++
			})
			d := ChatDecision{Client: c, Samples: len(tc.replies)}
			if len(tc.replies) == 3 {
				d.Samples = 0
			}
			got, err := d.Choose(context.Background(), LaneRequest{State: "context", Question: "question", Instructions: "instructions", Choices: []Choice{{"a", "apple"}, {"b", "brick"}}})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got.Pick != tc.pick || got.Status != tc.status || got.Probability != tc.prob || got.Margin != tc.margin {
				t.Fatalf("got %+v, %v", got, err)
			}
			if d.Name() != "chat:free-ai" {
				t.Fatal(d.Name())
			}
		})
	}
}

func TestChatRetryLimit(t *testing.T) {
	calls := 0
	c, delays := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"busy"}}`)
	})
	if _, err := c.complete(context.Background(), "test"); err == nil {
		t.Fatal("expected error")
	}
	if calls != 4 || fmt.Sprint(*delays) != "[2s 4s 5s]" {
		t.Fatalf("calls=%d delays=%v", calls, *delays)
	}
}

func TestChatVoiceFailure(t *testing.T) {
	empty, _ := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":""}}]}`)
	})
	if _, err := (ChatVoice{Client: empty}).Say(context.Background(), "hello"); err == nil {
		t.Fatal("empty voice accepted")
	}
	c, _ := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":{"message":"voice unavailable"}}`)
	})
	if _, err := (ChatVoice{Client: c}).Say(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "voice unavailable") {
		t.Fatalf("error = %v", err)
	}
	if _, err := (ChatDecision{Client: c}).Choose(context.Background(), LaneRequest{}); err == nil {
		t.Fatal("empty choices accepted")
	}
	if _, err := (ChatDecision{Client: c}).Choose(context.Background(), LaneRequest{Choices: []Choice{{"a", "apple"}}}); err == nil || !strings.Contains(err.Error(), "voice unavailable") {
		t.Fatalf("decision error = %v", err)
	}
}

func TestChatBudgetAndCancellation(t *testing.T) {
	calls := 0
	c, _ := fastChat(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"a"}}]}`)
	})
	if _, err := c.complete(context.Background(), strings.Repeat("x", 14336)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget error = %v", err)
	}
	if calls != 0 {
		t.Fatal("oversized request sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.complete(ctx, "hello"); err == nil {
		t.Fatal("cancelled call succeeded")
	}
	if err := chatSleep(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("sleep error = %v", err)
	}
}
