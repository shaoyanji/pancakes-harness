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

const releaseVersion = "0.2.4"

type config struct {
	addr      string
	sessionID string
	branchID  string
	jsonOut   bool
	startMode mode
}

type cli struct {
	cfg          config
	client       *http.Client
	in           io.Reader
	out          io.Writer
	err          io.Writer
	lastResult   []byte
	lastManifest []byte
}

type parsedLine struct {
	kind string
	arg  string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if shouldShowVersion(args) {
		fmt.Fprintf(stdout, "demo-cli %s\n", releaseVersion)
		return 0
	}
	if shouldShowHelp(args) {
		fmt.Fprint(stdout, usage())
		return 0
	}
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
	fmt.Fprintln(c.out, "commands: :help  :json on|off  :manifest  :trace  :agent <text>  :fork <name>  :replay  :status  :mode <turn|agent>  :quit")

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
		case "help":
			fmt.Fprint(c.out, helpText())
		case "json":
			if pl.arg == "on" {
				c.cfg.jsonOut = true
				fmt.Fprintln(c.out, "json=on")
			} else if pl.arg == "off" {
				c.cfg.jsonOut = false
				fmt.Fprintln(c.out, "json=off")
			} else {
				fmt.Fprintf(c.err, "usage: :json on|off\n")
			}
		case "manifest":
			if err := c.handleManifest(); err != nil {
				fmt.Fprintf(c.err, "manifest failed: %v\n", err)
			}
		case "trace":
			if err := c.handleTrace(); err != nil {
				fmt.Fprintf(c.err, "trace failed: %v\n", err)
			}
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
	case "help", "h", "?":
		return parsedLine{kind: "help"}
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
	case "json":
		return parsedLine{kind: "json", arg: strings.ToLower(arg)}
	case "manifest":
		return parsedLine{kind: "manifest"}
	case "trace", "last":
		return parsedLine{kind: "trace"}
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
	c.lastResult = append([]byte(nil), raw...)
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
		Contract    string   `json:"contract"`
		Answer      string   `json:"answer"`
		Trace       struct {
			ConsultManifest *struct {
				ActualBytes int `json:"actual_bytes"`
				ByteBudget  int `json:"byte_budget"`
				Selection   *struct {
					BudgetPressure           bool `json:"budget_pressure"`
					DominantInclusionReasons []struct {
						Reason string `json:"reason"`
						Count  int    `json:"count"`
					} `json:"dominant_inclusion_reasons"`
					DominantExclusionReasons []struct {
						Reason string `json:"reason"`
						Count  int    `json:"count"`
					} `json:"dominant_exclusion_reasons"`
				} `json:"selection"`
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
		c.lastManifest = append([]byte(nil), raw...)
	}
	selectorSummary := selectorSummaryFromManifest(out.Trace.ConsultManifest)

	if !out.Resolved {
		missing := strings.Join(out.Missing, ",")
		if missing == "" {
			missing = "-"
		}
		contract := out.Contract
		if contract == "" {
			contract = "agent_call.v1"
		}
		fmt.Fprintf(c.out, "[%s/%s] agent unresolved fp=%s contract=%s consult=%s missing=%s%s\n", out.SessionID, out.BranchID, fp, contract, consult, missing, selectorSummary)
		return nil
	}
	contract := out.Contract
	if contract == "" {
		contract = "agent_call.v1"
	}
	fmt.Fprintf(c.out, "[%s/%s] agent resolved fp=%s contract=%s consult=%s bytes=%s%s answer=%s\n", out.SessionID, out.BranchID, fp, contract, consult, bytesSummary, selectorSummary, out.Answer)
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
		Consults []struct {
			Outcome              string   `json:"outcome"`
			Role                 string   `json:"role"`
			BranchID             string   `json:"branch_id"`
			Fingerprint          string   `json:"fingerprint"`
			LeaderConsultEventID string   `json:"leader_consult_event_id"`
			Missing              []string `json:"missing"`
			ByteBudget           int      `json:"byte_budget"`
			ActualBytes          int      `json:"actual_bytes"`
			Selection            *struct {
				BudgetPressure           bool `json:"budget_pressure"`
				DominantInclusionReasons []struct {
					Reason string `json:"reason"`
					Count  int    `json:"count"`
				} `json:"dominant_inclusion_reasons"`
				DominantExclusionReasons []struct {
					Reason string `json:"reason"`
					Count  int    `json:"count"`
				} `json:"dominant_exclusion_reasons"`
			} `json:"selection"`
		} `json:"consults"`
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
	fmt.Fprintf(c.out, "replay session=%s events=%d branches=%s consults=%d\n", out.SessionID, out.State.EventCount, strings.Join(branchNames, ","), len(out.Consults))
	for _, consult := range out.Consults {
		fp := shortFingerprint(consult.Fingerprint)
		if len(consult.Missing) > 0 {
			fmt.Fprintf(c.out, "consult %s role=%s branch=%s fp=%s missing=%s\n", consult.Outcome, consult.Role, consult.BranchID, fp, strings.Join(consult.Missing, ","))
			continue
		}
		line := fmt.Sprintf("consult %s role=%s branch=%s fp=%s", consult.Outcome, consult.Role, consult.BranchID, fp)
		if consult.ByteBudget > 0 || consult.ActualBytes > 0 {
			line += fmt.Sprintf(" bytes=%d/%d", consult.ActualBytes, consult.ByteBudget)
		}
		if consult.LeaderConsultEventID != "" {
			line += " leader=" + consult.LeaderConsultEventID
		}
		line += selectorSummaryFromSelection(consult.Selection)
		fmt.Fprintln(c.out, line)
	}
	return nil
}

func (c *cli) handleManifest() error {
	if c.lastManifest == nil {
		fmt.Fprintln(c.err, "no manifest available; run an agent-call first")
		return nil
	}
	if c.cfg.jsonOut {
		fmt.Fprintln(c.out, string(c.lastManifest))
		return nil
	}
	var out struct {
		Trace struct {
			ConsultManifest *struct {
				SessionID     string            `json:"session_id"`
				BranchID      string            `json:"branch_id"`
				Fingerprint   string            `json:"fingerprint"`
				Mode          string            `json:"mode"`
				Scope         string            `json:"scope"`
				Refs          []string          `json:"refs"`
				Constraints   map[string]string `json:"constraints"`
				SelectedItems []struct {
					ID     string `json:"id"`
					Kind   string `json:"kind"`
					Ref    string `json:"ref"`
					Bytes  int    `json:"bytes"`
					Reason string `json:"reason"`
				} `json:"selected_items"`
				ByteBudget        int    `json:"byte_budget"`
				ActualBytes       int    `json:"actual_bytes"`
				Compacted         bool   `json:"compacted"`
				Truncated         bool   `json:"truncated"`
				SerializerVersion string `json:"serializer_version"`
				TaskSummary       string `json:"task_summary"`
				Selection         *struct {
					Included []struct {
						ID     string `json:"id"`
						Reason string `json:"reason"`
					} `json:"included"`
					Excluded []struct {
						ID     string `json:"id"`
						Reason string `json:"reason"`
					} `json:"excluded"`
					DominantInclusionReasons []struct {
						Reason string `json:"reason"`
						Count  int    `json:"count"`
					} `json:"dominant_inclusion_reasons"`
					DominantExclusionReasons []struct {
						Reason string `json:"reason"`
						Count  int    `json:"count"`
					} `json:"dominant_exclusion_reasons"`
					BudgetPressure bool `json:"budget_pressure"`
				} `json:"selection"`
			} `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(c.lastManifest, &out); err != nil {
		fmt.Fprintln(c.out, string(c.lastManifest))
		return nil
	}
	m := out.Trace.ConsultManifest
	if m == nil {
		fmt.Fprintln(c.err, "no manifest in last result")
		return nil
	}
	fmt.Fprintf(c.out, "manifest:\n")
	fmt.Fprintf(c.out, "  session=%s branch=%s mode=%s scope=%s\n", m.SessionID, m.BranchID, m.Mode, m.Scope)
	fmt.Fprintf(c.out, "  fingerprint=%s\n", m.Fingerprint)
	fmt.Fprintf(c.out, "  task=%s\n", m.TaskSummary)
	fmt.Fprintf(c.out, "  refs=%s\n", strings.Join(m.Refs, ", "))
	fmt.Fprintf(c.out, "  items=%d budget=%d actual=%d compacted=%v truncated=%v\n", len(m.SelectedItems), m.ByteBudget, m.ActualBytes, m.Compacted, m.Truncated)
	fmt.Fprintf(c.out, "  serializer=%s\n", m.SerializerVersion)
	for _, item := range m.SelectedItems {
		if item.Reason == "" {
			continue
		}
		fmt.Fprintf(c.out, "  selected %s=%s\n", item.ID, item.Reason)
	}
	if m.Selection != nil {
		fmt.Fprintf(c.out, "  selector%s\n", selectorSummaryFromManifestSelection(m.Selection))
		for _, item := range m.Selection.Excluded {
			fmt.Fprintf(c.out, "  excluded %s=%s\n", item.ID, item.Reason)
		}
	}
	if len(m.Constraints) > 0 {
		fmt.Fprintf(c.out, "  constraints:\n")
		for k, v := range m.Constraints {
			fmt.Fprintf(c.out, "    %s=%s\n", k, v)
		}
	}
	return nil
}

func selectorSummaryFromManifest(manifest *struct {
	ActualBytes int `json:"actual_bytes"`
	ByteBudget  int `json:"byte_budget"`
	Selection   *struct {
		BudgetPressure           bool `json:"budget_pressure"`
		DominantInclusionReasons []struct {
			Reason string `json:"reason"`
			Count  int    `json:"count"`
		} `json:"dominant_inclusion_reasons"`
		DominantExclusionReasons []struct {
			Reason string `json:"reason"`
			Count  int    `json:"count"`
		} `json:"dominant_exclusion_reasons"`
	} `json:"selection"`
}) string {
	if manifest == nil {
		return ""
	}
	return selectorSummaryFromSelection(manifest.Selection)
}

func selectorSummaryFromSelection(selection *struct {
	BudgetPressure           bool `json:"budget_pressure"`
	DominantInclusionReasons []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"dominant_inclusion_reasons"`
	DominantExclusionReasons []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"dominant_exclusion_reasons"`
}) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if len(selection.DominantInclusionReasons) > 0 && selection.DominantInclusionReasons[0].Reason != "" {
		parts = append(parts, "in="+selection.DominantInclusionReasons[0].Reason)
	}
	if len(selection.DominantExclusionReasons) > 0 && selection.DominantExclusionReasons[0].Reason != "" {
		parts = append(parts, "ex="+selection.DominantExclusionReasons[0].Reason)
	}
	if selection.BudgetPressure {
		parts = append(parts, "pressure=yes")
	}
	if len(parts) == 0 {
		return ""
	}
	return " selector[" + strings.Join(parts, " ") + "]"
}

func selectorSummaryFromManifestSelection(selection *struct {
	Included []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"included"`
	Excluded []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"excluded"`
	DominantInclusionReasons []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"dominant_inclusion_reasons"`
	DominantExclusionReasons []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"dominant_exclusion_reasons"`
	BudgetPressure bool `json:"budget_pressure"`
}) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if len(selection.DominantInclusionReasons) > 0 && selection.DominantInclusionReasons[0].Reason != "" {
		parts = append(parts, "in="+selection.DominantInclusionReasons[0].Reason)
	}
	if len(selection.DominantExclusionReasons) > 0 && selection.DominantExclusionReasons[0].Reason != "" {
		parts = append(parts, "ex="+selection.DominantExclusionReasons[0].Reason)
	}
	if selection.BudgetPressure {
		parts = append(parts, "pressure=yes")
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func (c *cli) handleTrace() error {
	if c.lastResult == nil {
		fmt.Fprintln(c.err, "no trace available; run an agent-call first")
		return nil
	}
	fmt.Fprintln(c.out, string(c.lastResult))
	return nil
}

func helpText() string {
	return `demo-cli commands:
  :help                 show this help
  :json on|off          toggle raw JSON output
  :manifest             show last agent-call consult manifest
  :trace, :last         show last agent-call raw JSON result
  :agent <text>         send /v1/agent-call
  :fork <name>          fork current branch
  :replay               replay session events
  :status               show current state
  :mode <turn|agent>    switch default mode
  :quit                 exit

flags:
  --addr        harness server address (default http://127.0.0.1:8080)
  --session-id  session id (default: generated)
  --branch-id   branch id (default: main)
  --mode        default mode: turn|agent (default: turn)
  --json        start with JSON output enabled
`
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

func shouldShowHelp(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

func shouldShowVersion(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-version", "--version", "version":
			return true
		}
	}
	return false
}

func usage() string {
	return `demo-cli 0.2.4

Thin demo shell over the pancakes-harness HTTP API.
It does not add runtime logic or expand the kernel surface.

Usage:
  demo-cli [flags]

Flags:
  --addr string
        harness server address (default http://127.0.0.1:8080)
  --session-id string
        existing session id; if empty a new demo id is generated
  --branch-id string
        active branch id (default main)
  --mode string
        default input mode: turn|agent (default turn)
  --json
        print raw response JSON

Commands:
  plain text
        sends /v1/turn or /v1/agent-call based on current mode
  :help
        show command help
  :json on|off
        toggle raw JSON output
  :manifest
        show last agent-call consult manifest
  :trace, :last
        show last agent-call raw JSON result
  :agent <text>
        sends /v1/agent-call
  :fork <name>
        sends /v1/branch/fork and switches active branch
  :replay
        fetches /v1/session/{session_id}/replay
  :status
        prints current client state
  :mode <turn|agent>
        switches default mode
  :quit
        exits the REPL

Examples:
  demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main
  demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main --mode agent
  demo-cli --version
`
}
