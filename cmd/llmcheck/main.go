package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"llm-bridge/internal/history"
	"llm-bridge/internal/llm"
	"llm-bridge/internal/protocol"
	"llm-bridge/internal/tools"
)

func main() {
	provider := llm.NewDeepSeekProviderFromEnv()
	modelUsed := provider.Model()

	messages := []llm.Message{
		{Role: string(llm.RoleUser), Content: "Use a ferramenta glob para listar os arquivos .go no diretório atual."},
	}

	const maxIterations = 5
	hadToolCall := false

	for i := 0; i < maxIterations; i++ {
		var chunks []string
		resp, err := provider.Chat(context.Background(), llm.ChatRequest{
			Messages: messages,
			Tools:    tools.All(),
		}, func(s string) {
			chunks = append(chunks, s)
			fmt.Println(string(protocol.NewChunk(s)))
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, string(protocol.NewError(err.Error())))
			os.Exit(1)
		}

		if len(chunks) == 0 && resp.Content != "" {
			fmt.Println(string(protocol.NewChunk(resp.Content)))
		}

		if len(resp.ToolCalls) > 0 {
			hadToolCall = true
		}

		for _, tc := range resp.ToolCalls {
			var input any = map[string]any{}
			if tc.Arguments != "" {
				input = json.RawMessage(tc.Arguments)
			}
			evt, err := protocol.NewToolCall(tc.ID, tc.Name, input)
			if err == nil {
				fmt.Println(string(evt))
			}
		}

		if len(resp.ToolCalls) == 0 {
			contextPct := 0.0
			metering := map[string]any{"credits": 0.0}
			fmt.Println(string(protocol.NewTurnEnd(resp.StopReason, &contextPct, metering, modelUsed)))

			if !hadToolCall {
				fmt.Fprintln(os.Stderr, "critério de aceite da etapa 7 não foi atingido: nenhuma tool call recebida")
				os.Exit(1)
			}

			// Passo 9: verificar sanitização do histórico
			sample := []llm.Message{
				{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "orphan", Name: "read"}}},
				{Role: llm.RoleUser, Content: "próximo turno"},
			}
			history.Sanitize(&sample)
			if len(sample) != 1 || sample[0].Role != llm.RoleUser {
				fmt.Fprintln(os.Stderr, "critério de aceite da etapa 9 não foi atingido: sanitização não removeu orphan tool_call")
				os.Exit(1)
			}

			// Passo 9: verificar remoção de blocos efêmeros
			ephemeralMsg := []llm.Message{
				{Role: llm.RoleUser, Content: history.MarkEphemeral("este texto deve ser removido")},
			}
			history.Sanitize(&ephemeralMsg)
			if len(ephemeralMsg) != 1 || ephemeralMsg[0].Content != "" {
				fmt.Fprintln(os.Stderr, "critério de aceite da etapa 9 não foi atingido: conteúdo efêmero não foi removido")
				os.Exit(1)
			}
			fmt.Println("OK: histórico sanitizado (orphan tool_call e conteúdo efêmero removidos)")

			fmt.Printf("OK: etapa 8 cumprida após %d iteração(ões)\n", i+1)
			return
		}

		assistantMsg := llm.Message{
			Role:      string(llm.RoleAssistant),
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		for _, tc := range resp.ToolCalls {
			// Simula a execução da tool com sucesso.
			result := map[string]any{"ok": true, "tool": tc.Name}
			out, _ := json.Marshal(result)
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    string(out),
				ToolCallID: tc.ID,
			})
		}
	}

	fmt.Fprintln(os.Stderr, "critério de aceite da etapa 8 não foi atingido: número máximo de iterações alcançado")
	os.Exit(1)
}
