package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type mode string

const (
	modeTurn  mode = "turn"
	modeAgent mode = "agent"
)

type config struct {
	addr      string
	sessionID string
	branchID  string
	jsonOut   bool
	startMode mode
}

type cli struct {
	cfg    config
	client *http.Client
	in     io.Reader
	out    io.Writer
	err    io.Writer
}

type parsedLine struct {
	kind string
	arg  string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 2
	}
	if cfg.sessionID == "" {
		cfg.sessionID = fmt.Sprintf("demo-%d", time.Now().UTC().UnixNano())
	}

	c := cli{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		in:  stdin,
		out: stdout,
		err: stderr,
	}
	return c.repl()
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("demo-cli", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfg := config{}
	var modeRaw string
	fs.StringVar(&cfg.addr, "addr", "http://127.0.0.1:8080", "harness server address")
	fs.StringVar(&cfg.sessionID, "session-id", "", "existing session id; if empty a new demo id is generated")
	fs.StringVar(&cfg.branchID, "branch-id", "main", "active branch id")
	fs.BoolVar(&cfg.jsonOut, "json", false, "print raw response JSON")
	fs.StringVar(&modeRaw, "mode", string(modeTurn), "default input mode: turn|agent")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg.addr = strings.TrimRight(strings.TrimSpace(cfg.addr), "/")
	cfg.sessionID = strings.TrimSpace(cfg.sessionID)
	cfg.branchID = strings.TrimSpace(cfg.branchID)
	modeRaw = strings.ToLower(strings.TrimSpace(modeRaw))
	if modeRaw != string(modeTurn) && modeRaw != string(modeAgent) {
		return config{}, errors.New("mode must be turn or agent")
	}
	cfg.startMode = mode(modeRaw)
	if cfg.addr == "" {
		return config{}, errors.New("addr is required")
	}
	return cfg, nil
}

func (c *cli) repl() int {
	currentMode := c.cfg.startMode
	fmt.Fprintf(c.out, "demo-cli session=%s branch=%s mode=%s addr=%s\n", c.cfg.sessionID, c.cfg.branchID, currentMode, c.cfg.addr)
	fmt.Fprintln(c.out, "commands: :agent <text>  :fork <name>  :replay  :status  :mode <turn|agent>  :quit")

	s := bufio.NewScanner(c.in)
	for {
		fmt.Fprint(c.out, "> ")
		if !s.Scan() {
			if s.Err() != nil {
				fmt.Fprintf(c.err, "read error: %v\n", s.Err())
				return 1
			}
			return 0
		}
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}

		pl := parseLine(line)
		switch pl.kind {
		case "quit":
			return 0
		case "status":
			fmt.Fprintf(c.out, "session=%s branch=%s mode=%s json=%t addr=%s\n", c.cfg.sessionID, c.cfg.branchID, currentMode, c.cfg.jsonOut, c.cfg.addr)
		case "mode":
			if pl.arg != string(modeTurn) && pl.arg != string(modeAgent) {
				fmt.Fprintln(c.err, "mode must be turn or agent")
				continue
			}
			currentMode = mode(pl.arg)
			fmt.Fprintf(c.out, "mode=%s\n", currentMode)
		case "fork":
			if pl.arg == "" {
				fmt.Fprintln(c.err, "usage: :fork <name>")
				continue
			}
			if err := c.handleFork(pl.arg); err != nil {
				fmt.Fprintf(c.err, "fork failed: %v\n", err)
			}
		case "replay":
			if err := c.handleReplay(); err != nil {
				fmt.Fprintf(c.err, "replay failed: %v\n", err)
			}
		case "agent":
			if pl.arg == "" {
				fmt.Fprintln(c.err, "usage: :agent <text>")
				continue
			}
			if err := c.handleAgent(pl.arg); err != nil {
				fmt.Fprintf(c.err, "agent-call failed: %v\n", err)
			}
		case "text":
			if currentMode == modeAgent {
				if err := c.handleAgent(pl.arg); err != nil {
					fmt.Fprintf(c.err, "agent-call failed: %v\n", err)
				}
			} else {
				if err := c.handleTurn(pl.arg); err != nil {
					fmt.Fprintf(c.err, "turn failed: %v\n", err)
				}
			}
		default:
			fmt.Fprintf(c.err, "unknown command: %s\n", pl.kind)
		}
	}
}

func parseLine(line string) parsedLine {
	line = strings.TrimSpace(line)
	if line == "" {
		return parsedLine{kind: "text", arg: ""}
	}
	if !strings.HasPrefix(line, ":") {
		return parsedLine{kind: "text", arg: line}
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(line, ":"))
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return parsedLine{kind: "text", arg: ""}
	}
	name := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(strings.TrimPrefix(cmd, parts[0]))
	}
	switch name {
	case "q", "quit", "exit":
		return parsedLine{kind: "quit"}
	case "status":
		return parsedLine{kind: "status"}
	case "replay":
		return parsedLine{kind: "replay"}
	case "fork":
		return parsedLine{kind: "fork", arg: arg}
	case "agent":
		return parsedLine{kind: "agent", arg: arg}
	case "mode":
		return parsedLine{kind: "mode", arg: strings.ToLower(arg)}
	default:
		return parsedLine{kind: name, arg: arg}
	}
}

func buildTurnRequest(sessionID, branchID, text string) map[string]any {
	return map[string]any{
		"session_id": strings.TrimSpace(sessionID),
		"branch_id":  strings.TrimSpace(branchID),
		"text":       strings.TrimSpace(text),
	}
}

func buildAgentCallRequest(sessionID, branchID, task string) map[string]any {
	return map[string]any{
		"session_id":  strings.TrimSpace(sessionID),
		"branch_id":   strings.TrimSpace(branchID),
		"task":        strings.TrimSpace(task),
		"allow_tools": false,
	}
}

func (c *cli) handleTurn(text string) error {
	body := buildTurnRequest(c.cfg.sessionID, c.cfg.branchID, text)
	raw, err := c.postJSON("/v1/turn", body)
	if err != nil {
		return err
	}
	if c.cfg.jsonOut {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	var out struct {
		SessionID string `json:"session_id"`
		BranchID  string `json:"branch_id"`
		Decision  string `json:"decision"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	fmt.Fprintf(c.out, "[%s/%s] %s\n", out.SessionID, out.BranchID, out.Answer)
	_ = out.Decision
	return nil
}

func (c *cli) handleAgent(task string) error {
	body := buildAgentCallRequest(c.cfg.sessionID, c.cfg.branchID, task)
	raw, err := c.postJSON("/v1/agent-call", body)
	if err != nil {
		return err
	}
	if c.cfg.jsonOut {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	var out struct {
		SessionID   string   `json:"session_id"`
		BranchID    string   `json:"branch_id"`
		Decision    string   `json:"decision"`
		Resolved    bool     `json:"resolved"`
		Missing     []string `json:"missing"`
		Fingerprint string   `json:"fingerprint"`
		Answer      string   `json:"answer"`
		Trace       struct {
			ConsultManifest *struct {
				ActualBytes int `json:"actual_bytes"`
				ByteBudget  int `json:"byte_budget"`
			} `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	fp := shortFingerprint(out.Fingerprint)
	consult := "no"
	bytesSummary := "-"
	if out.Trace.ConsultManifest != nil {
		consult = "yes"
		budget := out.Trace.ConsultManifest.ByteBudget
		actual := out.Trace.ConsultManifest.ActualBytes
		if budget > 0 {
			bytesSummary = fmt.Sprintf("%d/%d", actual, budget)
		} else {
			bytesSummary = fmt.Sprintf("%d/-", actual)
		}
	}

	if !out.Resolved {
		missing := strings.Join(out.Missing, ",")
		if missing == "" {
			missing = "-"
		}
		fmt.Fprintf(c.out, "[%s/%s] agent unresolved fp=%s consult=%s missing=%s\n", out.SessionID, out.BranchID, fp, consult, missing)
		return nil
	}
	fmt.Fprintf(c.out, "[%s/%s] agent resolved fp=%s consult=%s bytes=%s answer=%s\n", out.SessionID, out.BranchID, fp, consult, bytesSummary, out.Answer)
	return nil
}

func shortFingerprint(fp string) string {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		return "-"
	}
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}

func (c *cli) handleFork(child string) error {
	body := map[string]any{
		"session_id":       c.cfg.sessionID,
		"parent_branch_id": c.cfg.branchID,
		"child_branch_id":  strings.TrimSpace(child),
	}
	raw, err := c.postJSON("/v1/branch/fork", body)
	if err != nil {
		return err
	}
	if c.cfg.jsonOut {
		fmt.Fprintln(c.out, string(raw))
	} else {
		fmt.Fprintf(c.out, "forked %s -> %s\n", c.cfg.branchID, strings.TrimSpace(child))
	}
	c.cfg.branchID = strings.TrimSpace(child)
	return nil
}

func (c *cli) handleReplay() error {
	raw, err := c.get("/v1/session/" + c.cfg.sessionID + "/replay")
	if err != nil {
		return err
	}
	if c.cfg.jsonOut {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	var out struct {
		SessionID string            `json:"session_id"`
		Branches  map[string]string `json:"branches"`
		State     struct {
			EventCount int `json:"event_count"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Fprintln(c.out, string(raw))
		return nil
	}
	branchNames := make([]string, 0, len(out.Branches))
	for k := range out.Branches {
		branchNames = append(branchNames, k)
	}
	sort.Strings(branchNames)
	fmt.Fprintf(c.out, "replay session=%s events=%d branches=%s\n", out.SessionID, out.State.EventCount, strings.Join(branchNames, ","))
	return nil
}

func (c *cli) postJSON(path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.cfg.addr+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *cli) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.cfg.addr+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *cli) do(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var out struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &out) == nil && out.Error.Code != "" {
			return nil, fmt.Errorf("%s: %s", out.Error.Code, out.Error.Message)
		}
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
