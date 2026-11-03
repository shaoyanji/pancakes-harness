// Measures the deterministic gate against a replay of an adapter's predictions.
//
// The question it answers is not "does the gate make wrong calls right" — it
// cannot, and must not guess. It is: on the rows where no tool fits, does the gate
// abstain, and on the rows the model already answered exactly, does it stay out of
// the way.
//
//	go run ./scripts/gate-eval --root . \
//	  --eval scripts/needle-data/eval.pinned.jsonl \
//	  --preds scripts/needle-data/pred-pinned-ep3.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"pancakes-harness/internal/tooling"
)

type evalRow struct {
	Query   string         `json:"query"`
	Answers []tooling.Call `json:"answers"`
}

type predRow struct {
	Query     string         `json:"query"`
	Gold      []tooling.Call `json:"gold"`
	Predicted []tooling.Call `json:"predicted"`
	Exact     bool           `json:"exact"`
}

type tally struct {
	rows      int
	before    int // rows the model already abstained on
	after     int // rows that abstain once the gate is applied
	exact     int
	preserved int
	rejected  map[string]int // rule -> count
}

func main() {
	root := flag.String("root", ".", "repo root to index")
	evalPath := flag.String("eval", "scripts/needle-data/eval.pinned.jsonl", "eval rows")
	predsPath := flag.String("preds", "", "predictions to replay")
	verbose := flag.Bool("v", false, "print the deciding rule for every refused row")
	flag.Parse()

	if *predsPath == "" {
		fmt.Fprintln(os.Stderr, "gate-eval: --preds is required")
		os.Exit(2)
	}

	index, err := tooling.IndexFS(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate-eval: index %s: %v\n", *root, err)
		os.Exit(1)
	}
	gate := tooling.NewGate(registry(), index)
	evals, err := readEval(*evalPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate-eval: %v\n", err)
		os.Exit(1)
	}
	preds, err := readPreds(*predsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate-eval: %v\n", err)
		os.Exit(1)
	}
	if len(evals) != len(preds) {
		fmt.Fprintf(os.Stderr, "gate-eval: %d eval rows vs %d predictions\n", len(evals), len(preds))
		os.Exit(1)
	}

	var refusals, answering tally
	refusals.rejected = map[string]int{}
	answering.rejected = map[string]int{}
	var lost []string

	for i, row := range preds {
		gold := evals[i].Answers
		target := &answering
		if len(gold) == 0 {
			target = &refusals
		}
		kept, decisions := gate.CheckAll(row.Query, row.Predicted)
		target.rows++
		if len(row.Predicted) == 0 {
			target.before++
		}
		if len(kept) == 0 {
			target.after++
			if target == &answering {
				lost = append(lost, row.Query)
			}
			if *verbose {
				for _, decision := range decisions {
					if decision.Verdict == tooling.Abstain {
						fmt.Printf("  refused: %s | %s | %s\n", decision.Checks[len(decision.Checks)-1].Rule, decision.Reason, row.Query)
					}
				}
			}
		}
		if row.Exact {
			target.exact++
			if len(kept) > 0 {
				target.preserved++
			}
		}
		for _, decision := range decisions {
			if decision.Verdict == tooling.Abstain {
				target.rejected[decision.Checks[len(decision.Checks)-1].Rule]++
			}
		}
	}

	fmt.Printf("indexed files: %d\n\n", index.Len())
	fmt.Printf("rows the model already abstained on: %d of %d\n\n", answering.before+refusals.before, len(preds))

	fmt.Println("| set | rows | abstain before gate | abstain after gate |")
	fmt.Println("| :--- | ---: | ---: | ---: |")
	fmt.Printf("| no tool fits (gold = []) | %d | %d | %d |\n", refusals.rows, refusals.before, refusals.after)
	fmt.Printf("| answering (gold non-empty) | %d | %d | %d |\n", answering.rows, answering.before, answering.after)
	fmt.Println()

	fmt.Printf("exact rows preserved: %d of %d\n", answering.preserved, answering.exact)
	if kept := answering.rows - answering.after; kept > 0 {
		fmt.Printf("executed precision: %d of %d rows kept (%.1f%%)\n", answering.preserved, kept, 100*float64(answering.preserved)/float64(kept))
	}
	if len(lost) > 0 {
		fmt.Printf("answering rows refused by the gate: %d\n", len(lost))
		for _, query := range lost {
			fmt.Printf("  - %s\n", query)
		}
	}

	fmt.Println("\nabstain reasons")
	for _, name := range []string{"no tool fits", "answering"} {
		target := refusals
		if name != "no tool fits" {
			target = answering
		}
		if len(target.rejected) == 0 {
			continue
		}
		rules := make([]string, 0, len(target.rejected))
		for rule := range target.rejected {
			rules = append(rules, rule)
		}
		sort.Strings(rules)
		fmt.Printf("  %s:\n", name)
		for _, rule := range rules {
			fmt.Printf("    %-12s %d\n", rule, target.rejected[rule])
		}
	}
}

// registry mirrors the harness's built-in tool set, which is what the corpus and
// scripts/needle_tools.json are built from.
func registry() *tooling.Registry {
	reg := tooling.NewRegistry()
	tooling.RegisterBuiltinTools(reg)
	return reg
}

func readEval(path string) ([]evalRow, error) {
	rows := []evalRow{}
	err := eachLine(path, func(line []byte) error {
		var row evalRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

func readPreds(path string) ([]predRow, error) {
	rows := []predRow{}
	err := eachLine(path, func(line []byte) error {
		var row predRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

func eachLine(path string, fn func([]byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return scanner.Err()
}
