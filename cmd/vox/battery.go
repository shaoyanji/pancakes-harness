package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"pancakes-harness/internal/vox"
)

// runBattery measures the surface instead of using it: every case is a prompt
// with the call it should produce, and the score is how often the caller
// agreed. -repeat exists because the engine samples, so one pass cannot tell a
// good surface from a lucky draw.
func runBattery(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("battery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	casesPath := fs.String("cases", "", "battery cases (default $VOX_BATTERY or ./battery.json)")
	toolsPath := fs.String("tools", "", "tool manifest (default $VOX_TOOLS or ./tools.json)")
	replay := fs.String("replay", "", "answer every case from one recorded call instead of an engine")
	repeat := fs.Int("repeat", 1, "caller passes per case")
	threshold := fs.Float64("threshold", 0.8, "minimum tool and argument accuracy for exit 0")
	asJSON := fs.Bool("json", false, "the summary and every attempt as JSON")
	quiet := fs.Bool("quiet", false, "summary only, no per-attempt lines")
	timeout := fs.Duration("timeout", 15*time.Minute, "whole-battery timeout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox battery: %v\n", err)
		return 64
	}

	m, err := vox.LoadManifest(manifestPath(getenv, *toolsPath))
	if err != nil {
		fmt.Fprintf(stderr, "vox battery: %v\n", err)
		return 1
	}
	casesFile := strings.TrimSpace(*casesPath)
	if casesFile == "" {
		casesFile = envOr(getenv, "VOX_BATTERY")
	}
	if casesFile == "" {
		casesFile = "battery.json"
	}
	cases, err := vox.LoadCases(casesFile)
	if err != nil {
		fmt.Fprintf(stderr, "vox battery: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	sum, err := vox.RunBattery(ctx, buildCaller(getenv, *replay), m, cases, *repeat)
	if err != nil {
		if errors.Is(err, vox.ErrNoCaller) {
			fmt.Fprintln(stderr, "vox battery: no caller on this host — a battery measures the engine, so run it on arm64 or pass -replay")
			return 1
		}
		fmt.Fprintf(stderr, "vox battery: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			vox.Summary
			Accuracy    float64 `json:"accuracy"`
			ArgAccuracy float64 `json:"arg_accuracy"`
			CasesFile   string  `json:"cases_file"`
			Threshold   float64 `json:"threshold"`
		}{sum, sum.Accuracy(), sum.ArgAccuracy(), casesFile, *threshold})
	} else {
		printBattery(stdout, sum, *quiet)
	}

	if sum.Accuracy() < *threshold || sum.ArgAccuracy() < *threshold {
		return 1
	}
	return 0
}

func printBattery(w io.Writer, sum vox.Summary, quiet bool) {
	if !quiet {
		fmt.Fprintf(w, "%-20s %-4s %-13s %-13s %-4s %s\n", "case", "run", "want", "got", "args", "conf")
		for _, a := range sum.Log {
			want := a.Want
			if want == "" {
				want = "(no call)"
			}
			got := a.Tool
			switch {
			case a.Error != "":
				got = "!" + firstLine(a.Error)
			case a.NoCall:
				got = "(no call)"
			}
			args := "-"
			if a.ArgsChecked {
				args = "diff"
				if a.ArgsOK {
					args = "ok"
				}
			}
			fmt.Fprintf(w, "%-20s %-4d %-13s %-13s %-4s %.2f\n", a.Case, a.Run, want, truncateText(got, 13), args, a.Confidence)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "attempts   %d (%d cases x %d)\n", sum.Attempts, sum.Cases, sum.Repeat)
	fmt.Fprintf(w, "accuracy   %d/%d (%.2f)\n", sum.Correct, sum.Attempts, sum.Accuracy())
	fmt.Fprintf(w, "args       %d/%d\n", sum.ArgsCorrect, sum.ArgsChecked)
	fmt.Fprintf(w, "misses     wrong tool %d, no call %d, invalid %d\n", sum.WrongTool, sum.NoCall, sum.Invalid)
	keys := make([]string, 0, len(sum.PerTool))
	for k := range sum.PerTool {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		ts := sum.PerTool[k]
		parts = append(parts, fmt.Sprintf("%s %d/%d", k, ts.Got, ts.Want))
	}
	fmt.Fprintf(w, "per tool   %s\n", strings.Join(parts, "  "))
	if len(sum.Flaky) > 0 {
		fmt.Fprintf(w, "flaky      %s\n", strings.Join(sum.Flaky, " "))
	} else if sum.Repeat > 1 {
		fmt.Fprintln(w, "flaky      none")
	}
	fmt.Fprintf(w, "mean conf  %.2f\n", sum.MeanConfidence)
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
