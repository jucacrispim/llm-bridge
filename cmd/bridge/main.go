//go:build !test

package main

// notest

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"llm-bridge/knowledge"
	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/server"
)

const defaultLogFile = "/tmp/llm-bridge.log"

func main() {
	providerName := flag.String("provider", "deepseek", "LLM provider: 'deepseek' or 'google' (Gemini AI Studio)")
	modelName := flag.String("model", "", "model to use (overrides DEEPSEEK_MODEL / GOOGLE_MODEL)")
	thinking := flag.Bool("thinking", true, "enable thinking mode (uses deepseek-reasoner when no explicit model and provider deepseek; pass -thinking=false to disable). For deepseek defaults to DEEPSEEK_THINKING, for google GOOGLE_THINKING")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning_effort sent when thinking is on for deepseek (e.g. low/medium/high; default \"high\"); for google, a numeric thinkingConfig.thinkingBudget")
	debug := flag.Bool("debug", false, "enable debug logging to the default log file (kept for compatibility)")
	logFile := flag.String("logfile", "", "path of the log file; if set, all logs go there instead of stdout. Empty (the default) disables logging so the JSON-lines protocol on stdout stays clean")
	systemPromptFlag := flag.String("system-prompt", "", "path to a file containing the system prompt (or literal system prompt string)")
	aggressivePrune := flag.Bool("aggressive-prune", false, "collapse each completed tool-calling turn into just the user prompt + final answer, dropping the intermediate tool calls, tool results, and chain-of-thought from the history to save tokens and keep the prefix cacheable. Off by default.")
	knowledgeEnabled := flag.Bool("knowledge", true, "enable the project knowledge base (tool 'knowledge'); false disables it")
	knowledgeBase := flag.String("knowledge-base", "", "base directory for the project knowledge bases (default ~/.local/share/llm-bridge/knowledge_bases)")
	prune := flag.Bool("prune", false, "collapse reasoning and tool results, preserving file states (read/write/replace) as user snapshots. Off by default.")
	flag.Parse()

	if *aggressivePrune && *prune {
		fmt.Fprintf(os.Stderr, "error: -aggressive-prune and -prune are mutually exclusive\n")
		os.Exit(1)
	}

	if *providerName != "deepseek" && *providerName != "google" {
		fmt.Fprintf(os.Stderr, "unsupported provider: %s (supported: deepseek, google)\n", *providerName)
		os.Exit(1)
	}

	// Logging: if a log file is given, redirect all logs there. The -debug flag
	// (kept for compatibility, e.g. the llmcheck client) falls back to the
	// default log file. Otherwise no logs are emitted at all, so stdout stays
	// reserved for the JSON-lines protocol (no pollution for clients).
	switch {
	case *logFile != "":
		if err := setupFileLogging(*logFile); err != nil {
			fmt.Fprintf(os.Stderr, "logging setup failed: %v\n", err)
			os.Exit(1)
		}
	case *debug:
		if err := setupFileLogging(defaultLogFile); err != nil {
			fmt.Fprintf(os.Stderr, "logging setup failed: %v\n", err)
			os.Exit(1)
		}
	default:
		logger.SetOutput(io.Discard)
	}

	// Build a registry of BOTH providers so the bridge can switch provider per
	// request via the prompt's `provider` field (e.g. deepseek → google and
	// back). The --provider flag only selects the default/initial active one.
	providers := make(map[string]llm.LLMProvider)

	// deepseek provider.
	{
		// Resolve the model: an explicit --model flag (when deepseek is the
		// default) wins, then DEEPSEEK_MODEL. When no explicit model is
		// configured, the provider derives the model from the thinking mode
		// (deepseek-reasoner / deepseek-chat).
		apiKey := os.Getenv("DEEPSEEK_API_KEY")
		endpoint := os.Getenv("DEEPSEEK_URL")
		if endpoint == "" {
			endpoint = "https://api.deepseek.com/chat/completions"
		}
		model := ""
		if *providerName == "deepseek" {
			model = *modelName
		}
		if model == "" {
			model = os.Getenv("DEEPSEEK_MODEL")
		}
		var ds llm.LLMProvider
		if model != "" {
			ds = llm.NewDeepSeekProviderWithThinking(apiKey, endpoint, model, *thinking)
		} else {
			ds = llm.NewDeepSeekProviderAutoModel(apiKey, endpoint, *thinking)
		}
		// Reasoning effort: the --reasoning-effort flag wins, then
		// DEEPSEEK_REASONING_EFFORT, then the "high" default.
		effort := *reasoningEffort
		if effort == "" {
			effort = os.Getenv("DEEPSEEK_REASONING_EFFORT")
		}
		if effort != "" {
			ds.(*llm.DeepSeekProvider).SetReasoningEffort(effort)
		}
		providers["deepseek"] = ds
	}

	// google provider (Gemini AI Studio).
	{
		// Resolve the model: an explicit --model flag (when google is the
		// default) wins, then GOOGLE_MODEL / GEMINI_MODEL, then the gemini
		// default. Thinking is honored by GOOGLE_THINKING (or the --thinking
		// flag) and the budget by GOOGLE_THINKING_BUDGET / the numeric
		// --reasoning-effort.
		apiKey := os.Getenv("GOOGLE_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
		endpoint := os.Getenv("GOOGLE_URL")
		model := ""
		if *providerName == "google" {
			model = *modelName
		}
		if model == "" {
			model = os.Getenv("GOOGLE_MODEL")
		}
		if model == "" {
			model = os.Getenv("GEMINI_MODEL")
		}
		gp := llm.NewGoogleProviderWithThinking(apiKey, endpoint, model, *thinking)
		if !*thinking {
			gp.SetThinkingBudget(0)
		}
		if budget := *reasoningEffort; budget != "" {
			gp.SetThinkingBudget(llm.ParseBudget(budget, gp.ThinkingBudget()))
		} else if env := os.Getenv("GOOGLE_THINKING_BUDGET"); env != "" {
			gp.SetThinkingBudget(llm.ParseBudget(env, gp.ThinkingBudget()))
		}
		providers["google"] = gp
	}

	systemPrompt := ""
	if *systemPromptFlag != "" {
		if info, err := os.Stat(*systemPromptFlag); err == nil && !info.IsDir() {
			data, err := os.ReadFile(*systemPromptFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to read system prompt file %s: %v\n", *systemPromptFlag, err)
				os.Exit(1)
			}
			systemPrompt = string(data)
		} else {
			systemPrompt = *systemPromptFlag
		}
	}

	// Build the project-scoped knowledge base manager.
	//
	// -knowledge=false disables the KB outright (nil embedder → the server
	// treats it as disabled).
	//
	// When enabled, we try the real embedder. If loading it fails (model file
	// missing, or the build is without the knowledge_onnx tag, in which case
	// NewEmbedder always returns the "embeddings disabled" error / dlopen
	// failures at runtime), we log a warning and fall back to the disabled
	// embedder — the bridge keeps running, just without a KB (no crash).
	var kb *knowledge.Manager
	if !*knowledgeEnabled {
		logger.Infof("knowledge base disabled (flag -knowledge=false)")
		kb = knowledge.NewManager(nil, "")
	} else {
		baseDir := *knowledgeBase
		if baseDir == "" {
			baseDir = os.Getenv("KNOWLEDGE_BASE")
		}
		if baseDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "~"
			}
			baseDir = filepath.Join(home, ".local", "share", "llm-bridge", "knowledge_bases")
		}
		modelPath := os.Getenv("LLM_BRIDGE_KB_MODEL")
		if modelPath == "" {
			modelPath = filepath.Join(mustCacheDir(), "model.onnx")
		}
		tokenizerPath := os.Getenv("LLM_BRIDGE_KB_TOKENIZER")
		if tokenizerPath == "" {
			tokenizerPath = filepath.Join(mustCacheDir(), "tokenizer.json")
		}
		embed, err := knowledge.NewEmbedder(modelPath, tokenizerPath)
		if err != nil {
			logger.Warningf("knowledge base disabled: failed to load embedder: %v", err)
			kb = knowledge.NewManager(knowledge.DisabledEmbedder{}, "")
		} else {
			kb = knowledge.NewManager(embed, "")
		}
		logger.Infof("knowledge base enabled (base dir: %s)", baseDir)
	}

	if err := server.RunWithKnowledge(os.Stdin, os.Stdout, providers, *providerName,
		systemPrompt, *aggressivePrune, *prune, kb); err != nil {
		os.Exit(1)
	}
}

// mustCacheDir returns the llm-bridge cache dir (~/.cache/llm-bridge),
// creating it if needed. Default location for the downloaded KB model and
// tokenizer.
func mustCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = filepath.Join(homeDir(), ".cache")
	}
	dir = filepath.Join(dir, "llm-bridge")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// homeDir returns the user's home directory (or "~" as a last resort).
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~"
	}
	return home
}

// setupFileLogging redirects all logger output to logFile and enables the
// trace level, so the bridge starts dumping detailed logs to disk without
// polluting the JSON-lines protocol stream on stdout.
func setupFileLogging(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	logger.SetOutput(f)
	logger.SetLogLevel(logger.LevelTrace)
	logger.Tracef("debug logging enabled, writing to %s", logFile)
	return nil
}
