package vox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultChatURL     = "https://api.apisfreellm.com/v1"
	DefaultChatModel   = "free-ai"
	DefaultChatSamples = 3
)

// ChatClient serializes calls so samples share the provider's rate limit.
type ChatClient struct {
	URL   string
	Model string
	Key   string
	mu    sync.Mutex
	last  time.Time
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func (c *ChatClient) model() string {
	if c.Model != "" {
		return c.Model
	}
	return DefaultChatModel
}

func chatSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *ChatClient) complete(ctx context.Context, prompt string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now, sleep := c.now, c.sleep
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = chatSleep
	}
	base := c.URL
	if base == "" {
		base = DefaultChatURL
	}
	body, err := json.Marshal(map[string]any{
		"model": c.model(), "temperature": 0.7,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 120 * time.Second}
	for attempt := 0; ; attempt++ {
		if !c.last.IsZero() {
			if delay := time.Second - now().Sub(c.last); delay > 0 {
				if err := sleep(ctx, delay); err != nil {
					return "", err
				}
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.Key != "" {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		// Leave room for net/http's transport headers within the wire budget.
		if len(body)+len(req.URL.String())+len(c.Key)+512 > 14336 {
			return "", fmt.Errorf("chat request exceeds 14336-byte budget")
		}
		c.last = now()
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		parseErr := json.Unmarshal(raw, &result)
		message := string(raw)
		if result.Error != nil {
			message = result.Error.Message
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && result.Error == nil {
			if parseErr != nil {
				return "", fmt.Errorf("parse chat: %w", parseErr)
			}
			if len(result.Choices) == 0 {
				return "", fmt.Errorf("chat returned no content")
			}
			return strings.TrimSpace(result.Choices[0].Message.Content), nil
		}
		delay := time.Duration(2<<attempt) * time.Second
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		retry := resp.StatusCode == http.StatusServiceUnavailable
		if resp.StatusCode == http.StatusTooManyRequests {
			after, err := strconv.Atoi(resp.Header.Get("Retry-After"))
			retry = err == nil && after >= 0 && after <= 5
			if time.Duration(after)*time.Second > delay {
				delay = time.Duration(after) * time.Second
			}
			if strings.Contains(strings.ToLower(message), "daily") {
				retry = false
			}
		}
		if !retry || attempt >= 3 {
			return "", fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, message)
		}
		if err := sleep(ctx, delay); err != nil {
			return "", err
		}
	}
}

type ChatDecision struct {
	Client  *ChatClient
	Samples int
}

func (c ChatDecision) Name() string { return "chat:" + c.Client.model() }

func (c ChatDecision) Choose(ctx context.Context, req LaneRequest) (Verdict, error) {
	if len(req.Choices) == 0 {
		return Verdict{}, fmt.Errorf("chat decision needs choices")
	}
	n := c.Samples
	if n <= 0 {
		n = DefaultChatSamples
	}
	body, err := json.Marshal(req.Request())
	if err != nil {
		return Verdict{}, err
	}
	votes := make(map[string]int)
	for i := 0; i < n; i++ {
		answer, err := c.Client.complete(ctx, "Choose exactly one criteria label for lane. Reply with only that label, no punctuation or explanation.\n"+string(body))
		if err != nil {
			return Verdict{}, err
		}
		for _, choice := range req.Choices {
			if answer == choice.Label {
				votes[answer]++
				break
			}
		}
	}
	var pick string
	top, second := 0, 0
	for _, choice := range req.Choices {
		v := votes[choice.Label]
		if v > top {
			pick, top, second = choice.Label, v, top
		} else if v > second {
			second = v
		}
	}
	if top == 0 {
		return Verdict{}, fmt.Errorf("chat decision returned no valid labels")
	}
	status := "needs_review"
	// A strict majority of all samples is required; invalid votes still cost confidence.
	if top*2 > n {
		status = "selected"
	}
	return Verdict{Status: status, Pick: pick, Probability: float64(top) / float64(n), Margin: float64(top-second) / float64(n)}, nil
}

type ChatVoice struct{ Client *ChatClient }

func (c ChatVoice) Name() string { return "chat:" + c.Client.model() }

func (c ChatVoice) Say(ctx context.Context, prompt string) (string, error) {
	text, err := c.Client.complete(ctx, prompt)
	if err == nil && text == "" {
		return "", fmt.Errorf("chat returned no content")
	}
	return text, err
}
