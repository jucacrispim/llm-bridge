# Knowledge base específica do projeto llm-bridge

Seed **por projeto** — só entra na KB quando o projeto atual é `llm-bridge`.

Este conteúdo é **referencial**: o modelo deve buscá-lo via `knowledge search`
quando precisar de detalhes de arquitetura/implementação, em vez de assumir.

## Visão geral

- O llm-bridge é um servidor Go (package `server/`) que expõe um protocolo de
  linha: `prompt`, `tool_result`, `cancel`, `set_cwd`, `set_knowledge_bases`,
  `quit`. Eventos de saída: `ready`, `chunk`, `thinking`, `tool_call`,
  `turn_end`, `usage_delta`, `cancelled`, `error`.
- Providers de LLM em `llm/` (`deepseek.go` e `google.go`), cada um com suas
  env vars. `cmd/bridge/main.go` constrói o registry de providers e o entry
  point do server (`server.RunWithKnowledge`).
- O `system prompt` (fresco em toda requisição, não persiste no histórico)
  instrui o modelo a só usar tools quando o pedido exigir inspecionar/modificar
  o projeto ou rodar comando, e a responder conversa casual com texto puro.

## Overrides per-request (persistem entre prompts)

O `prompt` aceita campos com MAIOR prioridade que os valores globais e que
**persistem** para os próximos prompts até serem sobrescritos: `provider`,
`model`, `thinking`, `reasoning_effort` (e `system`/`system_prompt`).

- `model` → `st.modelOverride` → `ChatRequest.Model`.
- `thinking` (bool) → `st.thinkingOverride` → alterna on/off.
- `reasoning_effort` (string) → `st.reasoningEffortOverride` → override
  per-request; string vazia desabilita o envio; omitido mantém o valor global.
- `provider` → troca o provider ativo (`st.providerName`) p/ este e os próximos
  prompts. Requer registry (senão `provider switching not available`); nome
  fora do registry → `unknown provider`.
- `system`/`system_prompt` → `st.systemOverride`.

Valores globais (base quando não há override): flag CLI `--model`,
`--thinking`, `--reasoning-effort`, e env `DEEPSEEK_MODEL`/`DEEPSEEK_REASONING_EFFORT`
(default `high`), `GOOGLE_MODEL`, etc.

## DeepSeek (`llm/deepseek.go`)

- `resolveModel()` resolve nesta ordem: (1) modelo explícito da requisição;
  (2) modelo explícito do provider (`--model`/`DEEPSEEK_MODEL`); (3) modelo
  derivado do modo thinking (`deepseek-reasoner`/`deepseek-chat`).
- Thinking estruturado (não só bool): o `deepseek-v4-flash` raciocina por
  padrão. Envia `"thinking":{"type":"enabled"|"disabled"}` + `reasoning_effort`
  (top-level). `reasoning_effort` só é enviado quando thinking está on.
- **Restrição dura**: o DeepSeek exige `reasoning_content` de volta em toda
  mensagem de assistant que fez tool call — senão HTTP 400. Por isso o modo
  `-prune` reloca o conteúdo de arquivo para mensagens de usuário (role user),
  que não carregam reasoning.

## Google (`llm/google.go`) — Gemini AI Studio

- Estrutura espelha `deepseek.go`. Endpoint
  `POST {url}/models/{model}:streamGenerateContent?alt=sse&key={key}`.
- `contents` com roles `user`/`model`; system vai em `system_instruction`
  (não existe role `system` em contents).
- Tools via `tools[].functionDeclarations[]`; tool calls em part
  `{"functionCall":...}` e respostas `{"functionResponse":...}` em role user.
  **Gemini não tem tool_call_id** → o provider usa o nome da função como
  `ToolCall.ID` (o server casa `tool_result` por esse id).
- Reasoning/thinking: part com `thought:true` no stream → `OnReasoning`.
  `generationConfig.thinkingConfig.thinkingBudget` controla on/off (0 desliga).
- Com thinking ligado, toda part `functionCall` deve levar um `thought_signature`
  (base64) emitido pelo modelo e ecoado de volta no reenvio do histórico — senão
  HTTP 400 (`ToolCall.ThoughtSignature` em `llm/interface.go`).

## Tool cycle (`server/server.go` — `runToolCycle`)

- Cada iteração chama `activeProvider().Chat(...)` com `historySnapshot()`,
  `tools.All()` e `effectiveSystem()`.
- Tokens de **cada iteração** (tool calls e resposta final) são acumulados em
  `turnPromptTokens`/`turnCompletionTokens` e reportados no `turn_end`/
  `usage_delta` no fecho do turno (soma do ciclo todo).
- Tool calls `knowledge` são resolvidas **localmente** (fire-and-forget): não
  entram em `pendingToolIDs`, não esperam o cliente; se todas forem internas, o
  loop volta e chama `Chat` de novo.
- `tool_result` só vale com `pendingToolIDs`; duplicado → erro; `tool_result`
  atrasado de um turno cancelado é ignorado silenciosamente.
- No fecho do turno: se `-prune` → `pruneTurn()`; se `-aggressive-prune` →
  `collapseTurn()`; depois `sanitizeHistory()`.

## Modos de poda do histórico

- **`-aggressive-prune`**: colapsa cada turno com tools em só prompt + resposta
  final, descartando tool_calls, tool results e reasoning intermediários.
- **`-prune`** (exclusivo com `-aggressive-prune`): colapsa reasoning + tools de
  grep/shell/glob/code, mas **preserva** o estado de arquivos como snapshots de
  usuário imutáveis:
  - `read`/`write` → `<State <path> <DATETIME>>conteúdo</State>` (content do
    `tool_result` no read; content do argumento `write`).
  - `search_replace` → `<Replace <path> <DATETIME>>search:...---replace:...</Replace>`
    (delta).
  - Regra de redução por path: baseline = último `read`/`write` do path; mantém a
    baseline + replaces posteriores; descarta o que vem antes.
  - Requisito DeepSeek-safe: sem mensagem de assistant com tool call no
    histórico pós-colapso.

## Cancel defensivo e thread-safety

- Flag `st.cancelled` (guardado por `cancelMut`): `tool_result` atrasado de turno
  cancelado é ignorado (cliente pode enfileirar `tool_result` antes do próprio
  `cancel` ao negar uma tool).
- Cancel preserva o histórico (prompt + output parcial), mas `sanitizeHistory()`
  remove mensagens assistant com tool_calls órfãs (sem tool result) — senão o
  provider rejeita com HTTP 400 no próximo prompt.
- Todos os acessos a `st.history` passam por helpers com `historyMut sync.Mutex`
  (`appendUser`, `appendAssistant`, `appendToolResult`, `sanitizeHistory`,
  `historySnapshot`, `historyLen`).

## Knowledge base (`knowledge/`)

- Pacote `knowledge/`: `index.go` (interface `Index` + `bruteForce` em Go puro,
  sem gonum), `embed.go` (interface `Embedder` + prefixos E5 `query:`/`passage:`),
  `embed_onnx.go` (impl ONNX real, tag `knowledge_onnx`), `embed_disabled.go`
  (stub), `manager.go` (persistência), `seed.go`.
- Modelo `intfloat/multilingual-e5-small` (384 dims, multilíngue). Busca = dot
  product (Go puro, vetores normalizados).
- KB por projeto: chave derivada do `cwd`; armazenamento em
  `~/.local/share/llm-bridge/knowledge_bases/<projeto>/data.json`
  (schema `[{id, payload:{label,text}, vector}]`).
- Tool `knowledge` (`tools.All()`): `show` | `search` | `add` | `delete` |
  `reset` (query, label, text, limit). Fire-and-forget em `runToolCycle`.
  - `add` é **upsert por label**: re-adicionar um label existente **substitui** o
    texto/vetor (label é a chave; o modelo corrige a própria nota re-`add`).
  - `delete <label>` remove uma nota (label inexistente → aviso "not found").
  - `reset` limpa a KB e **reconstrói a partir do seed** (fonte da verdade do
    conteúdo arquitetural), descartando adds manuais.
- Primeiro turno: quando KB habilitada, injeta uma linha mínima
  `Knowledge base do projeto "X" disponível (tool: knowledge).` via
  `state.projectKBName()`.
- Build: `make build` (default, sem KB) / `make build-kb`
  (`CGO_ENABLED=1 go build -tags knowledge_onnx`). `scripts/fetch_kb.sh` baixa
  `libonnxruntime.so` + modelo + tokenizer pra `~/.cache/llm-bridge/`.
- **dlopen falho NÃO quebra**: usa `DisabledEmbedder` com warning (o modelo deve
  tratar resultado `error: knowledge: embeddings disabled`).

## Contexto (`context/` e `.llm-bridge`)

- `context.Load(cwd)` lê contexto geral `~/.llm-bridge/*.md` + local
  `<cwd>/.llm-bridge/*.md`, geral antes de local, ordem alfabética. Cada arquivo
  vira uma mensagem de usuário persistente com markup
  `--- CONTEXT ENTRY BEGIN ---[/path]conteúdo--- CONTEXT ENTRY END ---`.
- `set_knowledge_bases` é só metadata (nome/path) ligada ao `cwd` via
  `state.projectKBName()`; não é removida do protocolo.
