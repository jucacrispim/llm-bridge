// Copyright 2026 Juca Crispim <juca@poraodojuca.dev>
//
// This file is part of llm-bridge.
//
// llm-bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// llm-bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with llm-bridge. If not, see <http://www.gnu.org/licenses/>.

//go:build !test

package main

// notest

import (
	"errors"
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
	providerName := flag.String("provider", "deepseek", "LLM provider: 'deepseek', 'google' (Gemini AI Studio), 'anthropic' (Claude) or 'openai' (Responses API)")
	modelName := flag.String("model", "", "model to use (overrides DEEPSEEK_MODEL / GOOGLE_MODEL / ANTHROPIC_MODEL)")
	thinking := flag.Bool("thinking", true, "enable thinking mode (uses deepseek-flash when no explicit model and provider deepseek; pass -thinking=false to disable). For deepseek defaults to DEEPSEEK_THINKING, for google GOOGLE_THINKING, for anthropic ANTHROPIC_THINKING, for openai OPENAI_THINKING")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning effort sent when thinking is on: for deepseek a level (low/medium/high), for anthropic a level (low/medium/high/xhigh/max) or a numeric thinking budget, for google a numeric thinkingConfig.thinkingBudget, for openai a level (low/medium/high/xhigh/max)")
	debug := flag.Bool("debug", false, "enable debug logging to the default log file (kept for compatibility)")
	logFile := flag.String("logfile", "", "path of the log file; if set, all logs go there instead of stdout. Empty (the default) disables logging so the JSON-lines protocol on stdout stays clean")
	systemPromptFlag := flag.String("system-prompt", "", "path to a file containing the system prompt (or literal system prompt string)")
	aggressivePrune := flag.Bool("aggressive-prune", false, "collapse each completed tool-calling turn into just the user prompt + final answer, dropping the intermediate tool calls, tool results, and chain-of-thought from the history to save tokens and keep the prefix cacheable. Off by default.")
	knowledgeEnabled := flag.Bool("knowledge", true, "enable the knowledge base (tool 'knowledge'); false disables it")
	knowledgeBase := flag.String("knowledge-base", "", "base directory for the knowledge bases (default ~/.local/share/llm-bridge/knowledge_bases)")
	kbScope := flag.String("kb-scope", "global", "which knowledge base(s) to use: 'global' (default, one KB shared across projects), 'project' (one KB per cwd project) or 'both' (both at once). Can be overridden per prompt via kb_scope")
	kbLoad := flag.String("kb-load", "lazy", "when to load the knowledge base model: 'lazy' (default, load on first use so the server starts fast) or 'eager' (load at startup; failure disables the KB with a warning)")
	prune := flag.Bool("prune", false, "collapse reasoning and tool results, preserving file states (read/write/replace) as user snapshots. Off by default.")
	populateKB := flag.String("populate-project-kb", "", "populate a knowledge base from a seed directory and exit (standalone mode; requires the ONNX embedder). Mutually exclusive with running the server.")
	projectName := flag.String("project", "", "knowledge base key used with -populate-project-kb: a project name, or _global for the shared KB")
	sbCfg := registerSandboxFlags()
	flag.Parse()

	// Standalone mode: populate a project's knowledge base from a seed dir and
	// exit. It does NOT enter the server loop. Uses the ONNX embedder
	// (make build-kb) to embed every *.md in the seed dir (global + per-project)
	// and does a FULL rebuild (wipe + re-embed), so edits to the seed are
	// reflected. The seed stays decoupled from the binary: it is only read at
	// populate time.
	if *populateKB != "" {
		if err := populateProjectKB(*populateKB, *projectName, *knowledgeBase); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := sbCfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *aggressivePrune && *prune {
		fmt.Fprintf(os.Stderr, "error: -aggressive-prune and -prune are mutually exclusive\n")
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

	// A broken override keeps the embedded data, so the bridge still boots.
	if path, err := llm.LoadUserModels(); err != nil {
		logger.Warningf("models file ignored: %v", err)
	} else if path != "" {
		logger.Infof("models loaded from %s", path)
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
		// (deepseek-flash / deepseek-chat).
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

	// anthropic provider (Claude).
	{
		// Resolve the model: an explicit --model flag (when anthropic is the
		// default) wins, then ANTHROPIC_MODEL, then the claude default. The
		// thinking mode comes from ANTHROPIC_THINKING / the --thinking flag, and
		// max_tokens from ANTHROPIC_MAX_TOKENS (the API requires the field).
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		endpoint := os.Getenv("ANTHROPIC_URL")
		model := ""
		if *providerName == "anthropic" {
			model = *modelName
		}
		if model == "" {
			model = os.Getenv("ANTHROPIC_MODEL")
		}
		ap := llm.NewAnthropicProviderWithThinking(apiKey, endpoint, model, *thinking)
		if version := os.Getenv("ANTHROPIC_VERSION"); version != "" {
			ap.SetVersion(version)
		}
		if maxTokens := os.Getenv("ANTHROPIC_MAX_TOKENS"); maxTokens != "" {
			ap.SetMaxTokens(llm.ParseBudget(maxTokens, ap.MaxTokens()))
		}
		// A number is a thinking budget, a word is the default effort. The
		// --reasoning-effort flag wins over the environment.
		effort := *reasoningEffort
		if effort == "" {
			effort = os.Getenv("ANTHROPIC_THINKING_BUDGET")
		}
		if effort != "" {
			if budget := llm.ParseBudget(effort, -1); budget >= 0 {
				ap.SetThinkingBudget(budget)
			} else if llm.ValidReasoningLevel(effort) {
				ap.SetEffort(effort)
			}
		}
		providers["anthropic"] = ap
	}

	// openai provider (Responses API).
	{
		// Resolve the model: an explicit --model flag (when openai is the
		// default) wins, then OPENAI_MODEL, then the gpt default. OPENAI_THINKING
		// / the --thinking flag picks the thinking mode, OPENAI_REASONING_SUMMARY
		// turns the reasoning summary on, and OPENAI_MAX_OUTPUT_TOKENS caps the
		// output.
		apiKey := os.Getenv("OPENAI_API_KEY")
		endpoint := os.Getenv("OPENAI_URL")
		model := ""
		if *providerName == "openai" {
			model = *modelName
		}
		if model == "" {
			model = os.Getenv("OPENAI_MODEL")
		}
		op := llm.NewOpenAIProviderWithThinking(apiKey, endpoint, model, *thinking)
		op.SetReasoningSummary(os.Getenv("OPENAI_REASONING_SUMMARY"))
		if maxTokens := os.Getenv("OPENAI_MAX_OUTPUT_TOKENS"); maxTokens != "" {
			op.SetMaxOutputTokens(llm.ParseBudget(maxTokens, 0))
		}
		// One knob for both providers: a level is the effort, and OpenAI has no
		// numeric reasoning budget, so a number is ignored here.
		effort := *reasoningEffort
		if effort == "" {
			effort = os.Getenv("OPENAI_REASONING_EFFORT")
		}
		if llm.ValidReasoningLevel(effort) {
			op.SetReasoningEffort(effort)
		}
		providers["openai"] = op
	}

	// The registry is the source of truth for the provider names. A hardcoded
	// list here drifts the day a provider is registered without it.
	if _, ok := providers[*providerName]; !ok {
		fmt.Fprintf(os.Stderr, "unsupported provider: %s (supported: deepseek, google, anthropic, openai)\n", *providerName)
		os.Exit(1)
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

	// Knowledge base wiring: resolve the base dir, the scope and the embedder.
	// The server builds the managers lazily on `set_cwd`. -kb-load picks when
	// the ONNX model is loaded: LazyEmbedder on first use (the default) or at
	// startup, where a failure disables the KB with a warning. See
	// docs/source/usage.rst.
	var kbBaseDir string
	var kbEmbed knowledge.Embedder
	if *knowledgeEnabled {
		if !knowledge.IsScope(*kbScope) {
			fmt.Fprintf(os.Stderr, "invalid -kb-scope %q: use global, project or both\n", *kbScope)
			os.Exit(1)
		}
		kbBaseDir = *knowledgeBase
		if kbBaseDir == "" {
			kbBaseDir = os.Getenv("KNOWLEDGE_BASE")
		}
		if kbBaseDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "~"
			}
			kbBaseDir = filepath.Join(home, ".local", "share", "llm-bridge", "knowledge_bases")
		}
		modelPath := os.Getenv("LLM_BRIDGE_KB_MODEL")
		if modelPath == "" {
			modelPath = filepath.Join(mustCacheDir(), "model.onnx")
		}
		tokenizerPath := os.Getenv("LLM_BRIDGE_KB_TOKENIZER")
		if tokenizerPath == "" {
			tokenizerPath = filepath.Join(mustCacheDir(), "tokenizer.json")
		}
		if *kbLoad == "eager" {
			embed, err := knowledge.NewEmbedder(modelPath, tokenizerPath)
			if err != nil {
				logger.Warningf("knowledge base disabled: failed to load embedder: %v", err)
				logger.Infof("knowledge base disabled (base dir: %s)", kbBaseDir)
				kbEmbed = knowledge.DisabledEmbedder{}
			} else {
				kbEmbed = embed
				logger.Infof("knowledge base enabled (scope: %s, base dir: %s)", *kbScope, kbBaseDir)
			}
		} else {
			kbEmbed = knowledge.NewLazyEmbedder(modelPath, tokenizerPath)
			logger.Infof("knowledge base enabled (lazy load, scope: %s, base dir: %s)", *kbScope, kbBaseDir)
		}
	} else {
		logger.Infof("knowledge base disabled (flag -knowledge=false)")
	}

	// Tool sandbox: every external process the bridge spawns (shell, grep,
	// hooks) and the llm-tools helper run under caged with the policy built for
	// them. nil (only with -disable-sandbox) means "run them as before".
	sb, err := sbCfg.toolSandbox()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox setup failed: %v\n", err)
		os.Exit(1)
	}

	// Curl sandbox: the curl tool is the only one with network access, and it
	// pays for it with a filesystem policy that grants no project, home or temp
	// path (see sandboxConfig.curlSandbox). nil (sandbox off, or -curl=false)
	// means the tool is not offered to the model.
	curlSb, err := sbCfg.curlSandbox()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox setup failed: %v\n", err)
		os.Exit(1)
	}

	if err := server.RunWithKnowledgeBase(os.Stdin, os.Stdout, providers, *providerName,
		systemPrompt, *aggressivePrune, *prune, kbBaseDir, *kbScope, kbEmbed,
		server.Sandboxes{Tools: sb, Curl: curlSb, ToolBin: sbCfg.toolBin, CurlHosts: sbCfg.curlHosts}); err != nil {
		os.Exit(1)
	}
}

// populateProjectKB implements the standalone -populate-project-kb mode: it
// fully rebuilds the given knowledge base from a seed directory and persists
// it, then returns (the caller exits). The seed dir layout follows EnsureSeeded:
// <seedDir>/*.md (global) plus <seedDir>/<project>/*.md (per-project); for the
// shared KB (project == "_global") only the global seeds are used. The rebuild
// is total: the existing data.json is wiped and re-embedded from the seed, so
// edits to the seed are reflected. Requires the ONNX embedder (knowledge_onnx
// build tag); a failure to load it is fatal here.
func populateProjectKB(seedDir, project, knowledgeBaseFlag string) error {
	if project == "" {
		return errors.New("-populate-project-kb requires -project")
	}
	if seedDir == "" {
		return errors.New("-populate-project-kb requires a seed directory")
	}

	baseDir := knowledgeBaseFlag
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
		return fmt.Errorf("load embedder (build with `make build-kb` and run fetch_kb.sh?): %w", err)
	}
	m, err := knowledge.Load(baseDir, project, embed)
	if err != nil {
		return fmt.Errorf("load project kb: %w", err)
	}
	m.SetSeedRoot(seedDir)
	if project == knowledge.GlobalKey {
		// The shared KB is seeded from the global seed files only (there is no
		// "_global" seed subdirectory).
		m.SetSeedProject("")
	}
	// Reset wipes the project's data.json and re-embeds from the seed (total
	// reconstruction, reflecting any seed edits).
	if err := m.Reset(); err != nil {
		return fmt.Errorf("rebuild kb from seed: %w", err)
	}
	fmt.Printf("populated knowledge base for project %q in %s (%d items)\n",
		project, filepath.Join(baseDir, project), m.Len())
	return nil
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
