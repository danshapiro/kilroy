package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/strongdm/kilroy/internal/attractor/engine"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "attractor":
		attractor(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  kilroy attractor run --graph <file.dot> --config <run.yaml> [--run-id <id>] [--logs-root <dir>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --logs-root <dir>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --cxdb <http_base_url> --context-id <id>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor validate --graph <file.dot>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] <requirements>")
}

func attractor(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		attractorRun(args[1:])
	case "resume":
		attractorResume(args[1:])
	case "validate":
		attractorValidate(args[1:])
	case "ingest":
		attractorIngest(args[1:])
	default:
		usage()
		os.Exit(1)
	}
}

func attractorRun(args []string) {
	var graphPath string
	var configPath string
	var runID string
	var logsRoot string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPath = args[i]
		case "--config":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--config requires a value")
				os.Exit(1)
			}
			configPath = args[i]
		case "--run-id":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--run-id requires a value")
				os.Exit(1)
			}
			runID = args[i]
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--logs-root requires a value")
				os.Exit(1)
			}
			logsRoot = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}

	if graphPath == "" || configPath == "" {
		usage()
		os.Exit(1)
	}

	dotSource, err := os.ReadFile(graphPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := engine.LoadRunConfigFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Default: no deadline. CLI runs (especially with provider CLIs) can take hours.
	ctx := context.Background()

	res, err := engine.RunWithConfig(ctx, dotSource, cfg, engine.RunOptions{
		RunID:    runID,
		LogsRoot: logsRoot,
		OnCXDBStartup: func(info engine.CXDBStartupInfo) {
			if info.UIURL == "" {
				return
			}
			if info.UIStarted {
				fmt.Fprintf(os.Stderr, "CXDB UI starting at %s\n", info.UIURL)
				return
			}
			fmt.Fprintf(os.Stderr, "CXDB UI available at %s\n", info.UIURL)
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("run_id=%s\n", res.RunID)
	fmt.Printf("logs_root=%s\n", res.LogsRoot)
	fmt.Printf("worktree=%s\n", res.WorktreeDir)
	fmt.Printf("run_branch=%s\n", res.RunBranch)
	fmt.Printf("final_commit=%s\n", res.FinalCommitSHA)
	if res.CXDBUIURL != "" {
		fmt.Printf("cxdb_ui=%s\n", res.CXDBUIURL)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if string(res.FinalStatus) == "success" {
		os.Exit(0)
	}
	os.Exit(1)
}

func attractorValidate(args []string) {
	var graphPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPath = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}
	if graphPath == "" {
		usage()
		os.Exit(1)
	}
	dotSource, err := os.ReadFile(graphPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, diags, err := engine.Prepare(dotSource)
	if err != nil {
		for _, d := range diags {
			fmt.Fprintf(os.Stderr, "%s: %s (%s)\n", d.Severity, d.Message, d.Rule)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("ok: %s\n", filepath.Base(graphPath))
	for _, d := range diags {
		fmt.Printf("%s: %s (%s)\n", d.Severity, d.Message, d.Rule)
	}
	os.Exit(0)
}

func attractorResume(args []string) {
	var logsRoot string
	var cxdbBaseURL string
	var contextID string
	var runBranch string
	var repoPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--logs-root requires a value")
				os.Exit(1)
			}
			logsRoot = args[i]
		case "--cxdb":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--cxdb requires a value")
				os.Exit(1)
			}
			cxdbBaseURL = args[i]
		case "--context-id":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--context-id requires a value")
				os.Exit(1)
			}
			contextID = args[i]
		case "--run-branch":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--run-branch requires a value")
				os.Exit(1)
			}
			runBranch = args[i]
		case "--repo":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--repo requires a value")
				os.Exit(1)
			}
			repoPath = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}
	if logsRoot == "" && (cxdbBaseURL == "" || contextID == "") && runBranch == "" {
		usage()
		os.Exit(1)
	}
	// Default: no deadline. Resume may replay long stages or rehydrate large artifacts.
	ctx := context.Background()
	var (
		res *engine.Result
		err error
	)
	switch {
	case logsRoot != "":
		res, err = engine.Resume(ctx, logsRoot)
	case cxdbBaseURL != "" && contextID != "":
		res, err = engine.ResumeFromCXDB(ctx, cxdbBaseURL, contextID)
	case runBranch != "":
		res, err = engine.ResumeFromBranch(ctx, repoPath, runBranch)
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("run_id=%s\n", res.RunID)
	fmt.Printf("logs_root=%s\n", res.LogsRoot)
	fmt.Printf("worktree=%s\n", res.WorktreeDir)
	fmt.Printf("run_branch=%s\n", res.RunBranch)
	fmt.Printf("final_commit=%s\n", res.FinalCommitSHA)
	if res.CXDBUIURL != "" {
		fmt.Printf("cxdb_ui=%s\n", res.CXDBUIURL)
	}

	if string(res.FinalStatus) == "success" {
		os.Exit(0)
	}
	os.Exit(1)
}
