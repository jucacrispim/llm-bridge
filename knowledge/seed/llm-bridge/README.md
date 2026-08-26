# Knowledge base específica do projeto llm-bridge

Seed **por projeto** — só entra na KB quando o projeto atual é `llm-bridge`.

- O llm-bridge é um servidor Go (package `server/`) que expõe um protocolo de
  linha: `prompt`, `tool_result`, `cancel`, etc.
- Providers de LLM em `llm/` (deepseek e google), cada um com suas env vars.
- Overrides per-request (`provider`, `model`, `thinking`, `reasoning_effort`)
  persistem entre prompts e têm maior prioridade que os valores globais.
- O sistema de knowledge base fica no package `knowledge/`.
