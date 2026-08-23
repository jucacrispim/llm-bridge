//go:build !test

package main

// notest

import (
	"flag"
	"fmt"
	"os"

	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/server"
)

const defaultLogFile = "/tmp/llm-bridge.log"

func main() {
	providerName := flag.String("provider", "deepseek", "LLM provider (only 'deepseek' supported)")
	modelName := flag.String("model", "", "model to use (overrides DEEPSEEK_MODEL)")
	debug := flag.Bool("debug", false, "enable debug logging to a file")
	logFile := flag.String("logfile", defaultLogFile, "path of the log file used in debug mode")
	flag.Parse()

	if *providerName != "deepseek" {
		fmt.Fprintf(os.Stderr, "unsupported provider: %s\n", *providerName)
		os.Exit(1)
	}

	if *debug {
		if err := setupDebugLogging(*logFile); err != nil {
			fmt.Fprintf(os.Stderr, "debug logging setup failed: %v\n", err)
			os.Exit(1)
		}
	}

	var provider llm.LLMProvider
	if *modelName != "" {
		apiKey := os.Getenv("DEEPSEEK_API_KEY")
		endpoint := os.Getenv("DEEPSEEK_URL")
		if endpoint == "" {
			endpoint = "https://api.deepseek.com/chat/completions"
		}
		provider = llm.NewDeepSeekProvider(apiKey, endpoint, *modelName)
	} else {
		provider = llm.NewDeepSeekProviderFromEnv()
	}

	if err := server.Run(os.Stdin, os.Stdout, provider); err != nil {
		os.Exit(1)
	}
}

// setupDebugLogging redirects all logger output to logFile and enables the
// debug level, so the bridge starts dumping detailed logs to disk without
// polluting the JSON-lines protocol stream on stdout.
func setupDebugLogging(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	logger.SetOutput(f)
	logger.SetLogLevel(logger.LevelTrace)
	logger.Tracef("debug logging enabled, writing to %s", logFile)
	return nil
}
