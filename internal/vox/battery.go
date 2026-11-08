package vox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Case is one prompt and the call it should produce. An empty Tool means the
// right answer is no call at all: a surface that answers arithmetic with a
// lookup is a surface that will one day run something by accident. Args is a
// subset — only the keys listed are compared, because a caller that supplies an
// extra useful argument has not made a mistake.
type Case struct {
	ID     string            `json:"id"`
	Prompt string            `json:"prompt"`
	Tool   string            `json:"tool"`
	Args   map[string]string `json:"args,omitempty"`
	Why    string            `json:"why,omitempty"`
}

type caseFile struct {
	Surface string `json:"surface,omitempty"`
	Cases   []Case `json:"cases"`
}

// LoadCases reads a battery. Ids are assigned rather than required, because a
// case is easier to add than to name and an unnamed case still has to be
// reported individually.
func LoadCases(path string) ([]Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read battery cases: %w", err)
	}
	var f caseFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse battery cases: %w", err)
	}
	if len(f.Cases) == 0 {
		return nil, errors.New("the battery has no cases")
	}
	seen := make(map[string]bool, len(f.Cases))
	for i := range f.Cases {
		c := &f.Cases[i]
		if strings.TrimSpace(c.Prompt) == "" {
			return nil, fmt.Errorf("case %d has no prompt", i+1)
		}
		if strings.TrimSpace(c.ID) == "" {
			c.ID = fmt.Sprintf("case-%d", i+1)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("case id %q is used twice", c.ID)
		}
		seen[c.ID] = true
	}
	return f.Cases, nil
}

// Attempt is one caller pass at one case. Repeat exists because the engine
// samples: one pass cannot tell a surface apart from a lucky draw.
type Attempt struct {
	Case        string            `json:"case"`
	Run         int               `json:"run"`
	Want        string            `json:"want"`
	Tool        string            `json:"tool,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	ArgsChecked bool              `json:"-"`
	ArgsOK      bool              `json:"args_ok,omitempty"`
	NoCall      bool              `json:"no_call,omitempty"`
	Error       string            `json:"error,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
}

// Correct reports whether this attempt produced the expected call. An expected
// no-call is only correct when the caller also reported no error: a caller that
// failed to parse itself has not chosen silence.
func (a Attempt) Correct(want string) bool {
	if strings.TrimSpace(want) == "" {
		return a.NoCall && a.Error == ""
	}
	return a.Error == "" && a.Tool == want
}

// ToolScore is the per-expected-tool tally, so a surface that is good at
// lookups and bad at silence is visible instead of averaged away.
type ToolScore struct {
	Want int `json:"want"`
	Got  int `json:"got"`
}

// Summary is one battery outcome: the counts a caller can gate on, plus the log
// a person needs to see which prompts went wrong.
type Summary struct {
	Cases          int                  `json:"cases"`
	Repeat         int                  `json:"repeat"`
	Attempts       int                  `json:"attempts"`
	Correct        int                  `json:"correct"`
	WrongTool      int                  `json:"wrong_tool"`
	NoCall         int                  `json:"no_call"`
	Invalid        int                  `json:"invalid"`
	ArgsChecked    int                  `json:"args_checked"`
	ArgsCorrect    int                  `json:"args_correct"`
	MeanConfidence float64              `json:"mean_confidence"`
	Flaky          []string             `json:"flaky"`
	PerTool        map[string]ToolScore `json:"per_tool"`
	Log            []Attempt            `json:"log"`
}

// Accuracy is the share of attempts that picked the expected tool. A gate reads
// this, not the mean confidence: a caller can be very sure and very wrong.
func (s Summary) Accuracy() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.Correct) / float64(s.Attempts)
}

// ArgAccuracy is the share of attempts that supplied the expected arguments,
// over the attempts whose case named any. A case with no expected arguments is
// not a silent pass, so the denominator is the checked count.
func (s Summary) ArgAccuracy() float64 {
	if s.ArgsChecked == 0 {
		return 1
	}
	return float64(s.ArgsCorrect) / float64(s.ArgsChecked)
}

// RunBattery calls the caller once per case per repeat and scores the result. A
// case naming a tool the manifest does not declare is an error rather than a
// miss: the battery drifted from the surface, and a drifted battery reports its
// own bug as the engine's.
func RunBattery(ctx context.Context, caller Caller, m Manifest, cases []Case, repeat int) (Summary, error) {
	if caller == nil {
		return Summary{}, ErrNoCaller
	}
	if repeat < 1 {
		repeat = 1
	}
	for _, c := range cases {
		if want := strings.TrimSpace(c.Tool); want != "" {
			if _, ok := m.byName(want); !ok {
				return Summary{}, fmt.Errorf("%w: case %s expects %s", ErrUnknownTool, c.ID, want)
			}
		}
	}

	sum := Summary{Cases: len(cases), Repeat: repeat, PerTool: make(map[string]ToolScore)}
	sigs := make(map[string]map[string]bool, len(cases))
	var confSum float64

	for _, c := range cases {
		for run := 1; run <= repeat; run++ {
			att := Attempt{Case: c.ID, Run: run, Want: c.Tool}
			call, ok, err := caller.Call(ctx, c.Prompt, m)
			switch {
			case err != nil:
				if errors.Is(err, ErrNoCaller) {
					return Summary{}, err
				}
				att.Error = err.Error()
			case !ok:
				att.NoCall = true
			default:
				att.Tool, att.Args, att.Confidence = call.Tool, call.Args, call.Confidence
				if len(c.Args) > 0 {
					att.ArgsChecked = true
					att.ArgsOK = argsMatch(c.Args, call.Args)
				}
			}

			sum.Attempts++
			confSum += att.Confidence
			want := strings.TrimSpace(c.Tool)
			bucket := want
			if bucket == "" {
				bucket = "(no call)"
			}
			ts := sum.PerTool[bucket]
			ts.Want++
			switch {
			case att.Correct(want):
				sum.Correct++
				ts.Got++
			case att.Error != "":
				sum.Invalid++
			case att.NoCall:
				sum.NoCall++
			default:
				sum.WrongTool++
			}
			sum.PerTool[bucket] = ts
			if att.ArgsChecked {
				sum.ArgsChecked++
				if att.ArgsOK {
					sum.ArgsCorrect++
				}
			}
			if sigs[c.ID] == nil {
				sigs[c.ID] = make(map[string]bool, repeat)
			}
			sigs[c.ID][signature(att)] = true
			sum.Log = append(sum.Log, att)
		}
	}

	if sum.Attempts > 0 {
		sum.MeanConfidence = confSum / float64(sum.Attempts)
	}
	for id, seen := range sigs {
		if len(seen) > 1 {
			sum.Flaky = append(sum.Flaky, id)
		}
	}
	sort.Strings(sum.Flaky)
	return sum, nil
}

// signature collapses an attempt to the thing a repeat is meant to test: the
// same tool is stable even when the confidence moved.
func signature(a Attempt) string {
	switch {
	case a.Error != "":
		return "error:" + a.Error
	case a.NoCall:
		return "no-call"
	default:
		return a.Tool
	}
}

// argsMatch is a subset comparison, trimmed and case-folded: a caller that
// writes "Frieren" for `host` has named the right machine, and a battery that
// called that a miss would measure spelling rather than tool choice.
func argsMatch(want, got map[string]string) bool {
	for k, v := range want {
		have, ok := got[k]
		if !ok {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(have), strings.TrimSpace(v)) {
			return false
		}
	}
	return true
}
