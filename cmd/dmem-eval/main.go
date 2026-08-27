package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/evaluation/longmemeval"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 || os.Args[1] != "longmemeval" {
		fmt.Fprintln(os.Stderr, "usage: dmem-eval longmemeval [flags]")
		os.Exit(2)
	}
	if err := run(ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dmem-eval longmemeval", flag.ContinueOnError)
	datasetPath := fs.String("dataset", "", "LongMemEval JSON dataset path")
	configPath := fs.String("config", "", "dMem config file path (empty uses $XDG_CONFIG_HOME/dmem/config.toml)")
	socket := fs.String("socket", os.Getenv("DMEM_SOCKET"), "dMem Unix socket (overrides --config)")
	callerID := fs.String("caller-id", "longmemeval", "dMem caller ID")
	runID := fs.String("run-id", time.Now().UTC().Format("20060102T150405Z"), "stable run ID")
	output := fs.String("output", "results/longmemeval/result.json", "result and resume file")
	hypotheses := fs.String("hypotheses", "", "optional official-compatible hypothesis JSONL")
	mode := fs.String("retrieval-mode", "hybrid", "keyword, semantic, or hybrid")
	limit := fs.Int("limit", 100, "maximum recalled memories (1-100)")
	maxChars := fs.Int("max-chars", 100000, "dMem recall character budget")
	cutoffsText := fs.String("cutoffs", "1,3,5,10,30,50,100", "comma-separated retrieval cutoffs")
	resume := fs.Bool("resume", false, "resume completed questions from output")
	answerModel := fs.String("answer-model", "", "optional OpenAI-compatible answer model")
	answerBaseURL := fs.String("answer-base-url", envDefault("LONGMEMEVAL_ANSWER_BASE_URL", os.Getenv("OPENAI_BASE_URL")), "answer API base URL")
	answerAPIKey := fs.String("answer-api-key", envDefault("LONGMEMEVAL_ANSWER_API_KEY", os.Getenv("OPENAI_API_KEY")), "answer API key")
	answerCutoff := fs.Int("answer-cutoff", 30, "retrieved memories supplied to answer model")
	judgeModel := fs.String("judge-model", "", "optional official-compatible judge model")
	judgeBaseURL := fs.String("judge-base-url", envDefault("LONGMEMEVAL_JUDGE_BASE_URL", os.Getenv("OPENAI_BASE_URL")), "judge API base URL")
	judgeAPIKey := fs.String("judge-api-key", envDefault("LONGMEMEVAL_JUDGE_API_KEY", os.Getenv("OPENAI_API_KEY")), "judge API key")
	timeout := fs.Duration("request-timeout", 2*time.Minute, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *datasetPath == "" {
		return errors.New("--dataset is required")
	}
	if *socket == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			cfg, err = config.Default()
			if err != nil {
				return fmt.Errorf("resolve dMem socket: provide --socket or --config: %w", err)
			}
		}
		*socket = cfg.Endpoint
	}
	cutoffs, err := parseCutoffs(*cutoffsText)
	if err != nil {
		return err
	}
	dataset, err := longmemeval.Load(*datasetPath)
	if err != nil {
		return err
	}
	client := longmemeval.NewUnixClient(*socket, *callerID, *timeout)
	var answerer, judge longmemeval.ChatModel
	if *answerModel != "" {
		answerer, err = longmemeval.NewOpenAIChatModel(*answerBaseURL, *answerAPIKey, *answerModel, *timeout)
		if err != nil {
			return fmt.Errorf("answer model: %w", err)
		}
	}
	if *judgeModel != "" {
		judge, err = longmemeval.NewOpenAIChatModel(*judgeBaseURL, *judgeAPIKey, *judgeModel, *timeout)
		if err != nil {
			return fmt.Errorf("judge model: %w", err)
		}
	}
	runner, err := longmemeval.NewRunner(client, answerer, judge, longmemeval.Config{RunID: *runID, OutputPath: *output, HypothesesPath: *hypotheses, RetrievalMode: *mode, Limit: *limit, MaxChars: *maxChars, Cutoffs: cutoffs, AnswerCutoff: *answerCutoff, Resume: *resume})
	if err != nil {
		return err
	}
	result, err := runner.Run(ctx, dataset)
	if err != nil {
		return err
	}
	fmt.Printf("LongMemEval: %d eligible, %d excluded; result=%s\n", result.Summary.EligibleQuestions, result.Summary.ExcludedQuestions, *output)
	return nil
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseCutoffs(value string) ([]int, error) {
	parts := strings.Split(value, ",")
	result := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, part := range parts {
		cutoff, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || cutoff < 1 || seen[cutoff] {
			return nil, fmt.Errorf("invalid cutoff %q", part)
		}
		seen[cutoff] = true
		result = append(result, cutoff)
	}
	return result, nil
}
