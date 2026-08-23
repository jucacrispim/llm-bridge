//go:build !test

package main

// notest

import (
	"flag"
	"fmt"
	"os"

	"llm-bridge/llm"
	"llm-bridge/server"
)

func main() {
	providerName := flag.String("provider", "deepseek", "LLM provider (only 'deepseek' supported)")
	modelName := flag.String("model", "", "model to use (overrides DEEPSEEK_MODEL)")
	flag.Parse()

	if *providerName != "deepseek" {
		fmt.Fprintf(os.Stderr, "unsupported provider: %s\n", *providerName)
		os.Exit(1)
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
