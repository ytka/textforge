package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/ytka/textforge/internal/ioutil"
	"github.com/ytka/textforge/internal/openai"
	"github.com/ytka/textforge/internal/runner"
	"github.com/ytka/textforge/internal/steps"
	"github.com/ytka/textforge/internal/tui"
)

var (
	ErrorAPIKeyFileNotFound = errors.New("API key file not found")
	c                       runner.Config
	rootCmd                 = &cobra.Command{
		Use:   "textforge",
		Short: "textforge is a tool designed to shape and transform text using OpenAI's GPT model.",
		Long:  "textforge is a tool designed to shape and transform text using OpenAI's GPT model.",
		RunE: func(_ *cobra.Command, args []string) error {
			if !checkAPIKeyFileExists() {
				return fmt.Errorf("%w: %s", ErrorAPIKeyFileNotFound, getAPIKeyFilePath())
			}

			inputFiles := args
			if c.InputFileList != "" {
				files, err := readInputFiles(c.InputFileList)
				if err != nil {
					return err
				}
				inputFiles = files
			}
			ctx := context.Background()
			return doRun(ctx, inputFiles, makeGAIFunc)
		},
	}
)

func init() {
	rootCmd.Version = "unknown-version"

	// Prompt options
	rootCmd.Flags().StringVarP(&c.Prompt, "prompt", "p", "", "Prompt text")
	rootCmd.Flags().StringVarP(&c.PromptPath, "prompt-path", "P", "", "Prompt file path")
	rootCmd.Flags().BoolVarP(&c.PromptOptimize, "prompt-optimize", "O", true, "Optimize prompt text")

	// Model options
	rootCmd.Flags().StringVarP(&c.Model, "model", "m", "gpt-4o", "model to use for text generation")
	rootCmd.Flags().IntVarP(&c.MaxTokens, "max-tokens", "t", 0, "Max tokens to generate")
	rootCmd.Flags().IntVar(&c.MaxCompletionRepeatCount, "max-completion-repeat-count", 1, "Max completion repeat count")

	// Stdout messages options
	rootCmd.Flags().BoolVarP(&c.DryRun, "dry-run", "D", false, "Dry run")
	rootCmd.Flags().BoolVarP(&c.Verbose, "verbose", "v", false, "Verbose output")
	rootCmd.Flags().BoolVarP(&c.Silent, "silent", "s", false, "Suppress output")
	rootCmd.Flags().BoolVarP(&c.ShowCost, "show-cost", "C", false, "Show cost of the text generation")
	rootCmd.Flags().BoolVarP(&c.Diff, "diff", "d", false, "Show diff of the input and output text")

	// Input file options
	rootCmd.Flags().StringVarP(&c.InputFileList, "input-file-list", "i", "", "Input file list")

	// Debug options
	rootCmd.Flags().StringVarP(&c.LogAPILevel, "log-api-level", "l", "", "API log level: info, debug")

	// Write file options
	rootCmd.Flags().BoolVarP(&c.Rewrite, "rewrite", "r", false, "Rewrite the input file with the result")
	rootCmd.Flags().StringVarP(&c.Outpath, "outpath", "o", "", "Output file path")
	rootCmd.Flags().BoolVarP(&c.UseFirstCodeBlock, "use-first-code-block", "f", false, "Use the first code block in the output text")
	rootCmd.Flags().BoolVarP(&c.Confirm, "confirm", "c", false, "Confirm before writing to file")
}

func Execute(version, commit, date, builtBy string) {
	var sb strings.Builder
	sb.WriteString(version)
	sb.WriteString(", commit ")
	sb.WriteString(commit)
	sb.WriteString(", built at ")
	sb.WriteString(date)
	sb.WriteString(", built by ")
	sb.WriteString(builtBy)
	rootCmd.Version = sb.String()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func makeGAIFunc(model string) (openai.GenerativeAIClient, error) {
	apikey, err := getAPIKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}
	var maxTokens *int
	if c.MaxTokens > 0 {
		maxTokens = &c.MaxTokens
	}
	return openai.New(apikey, model, c.LogAPILevel, maxTokens), nil
}

func readInputFiles(fileName string) ([]string, error) {
	data, err := os.ReadFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to read input files from %s: %w", fileName, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		files = append(files, line)
	}
	return files, nil
}

func showCosts(usageCosts []*openai.UsageCost) {
	totalUsageCost := openai.NewTotalUsageCost(usageCosts)
	if ok, cost := totalUsageCost.TotalTotalTokensCost(); ok {
		fmt.Printf("Total cost: $%f\n", cost)
	} else {
		fmt.Println("Total cost: unknown")
	}
}

func createProcessingCallbackFunc(enableTUI bool, rawOnAfterProcessing func(string, *steps.ShapeResult)) (func(string), func(string, *steps.ShapeResult)) {
	onBeforeProcessing := func(string) {}
	onAfterProcessing := rawOnAfterProcessing

	if enableTUI {
		var wg sync.WaitGroup
		var statusUI *tui.StatusUI
		onBeforeProcessing = func(inpath string) {
			wg.Add(1)
			input := "Stdin"
			if inpath != "-" {
				input = inpath
			}
			statusUI = tui.NewStatusUI(fmt.Sprintf("Processing... [%s]", input))
			go func() {
				defer wg.Done()
				if err := statusUI.Run(); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "failed to run status UI: %v\n", err)
				}
			}()
		}
		onAfterProcessing = func(inpath string, sr *steps.ShapeResult) {
			statusUI.Quit()
			statusUI = nil
			rawOnAfterProcessing(inpath, sr)
			wg.Wait()
		}
	}

	return onBeforeProcessing, onAfterProcessing
}

func doRun(ctx context.Context, inputFiles []string, makeGAIFunc func(model string) (openai.GenerativeAIClient, error)) error {
	r := runner.New(&c, inputFiles, makeGAIFunc, tui.Confirm)
	ropt, err := r.Setup()
	if err != nil {
		return fmt.Errorf("failed to setup runner: %w", err)
	}

	var usageCosts = make([]*openai.UsageCost, 0, len(inputFiles))
	rawOnAfterProcessing := func(_ string, sr *steps.ShapeResult) {
		if sr != nil {
			usageCosts = append(usageCosts, openai.NewUsageCost(sr.ChatCompletion))
		}
	}

	stdinPipeAvailable, err := ioutil.IsStdinPipe()
	if err != nil {
		return fmt.Errorf("failed to check if stdin is pipe: %w", err)
	}
	stdoutPipeAvailable, err := ioutil.IsStdoutPipeOrRedirect()
	if err != nil {
		return fmt.Errorf("failed to check if stdout is pipe: %w", err)
	}

	enableTUI := !c.Silent && !stdinPipeAvailable && !stdoutPipeAvailable
	onBeforeProcessing, onAfterProcessing := createProcessingCallbackFunc(enableTUI, rawOnAfterProcessing)
	if err := r.Run(ctx, ropt, onBeforeProcessing, onAfterProcessing); err != nil {
		return fmt.Errorf("failed to run: %w", err)
	}

	if c.ShowCost {
		showCosts(usageCosts)
	}

	return nil
}
