# Knowledge base do llm-bridge (seed global)

Este arquivo é um exemplo de seed **global** — conteúdo curado, genérico sobre o
uso do bridge, embebido na primeira carga de qualquer projeto.

- O bridge é um binário Go que conversa com modelos de linguagem via uma
  interface de linha, usando tools (read, grep, shell, ...) para inspecionar ou
  modificar o projeto.
- Responda conversa casual com texto puro; só use tools quando o pedido exigir
  inspecionar/modificar código ou rodar comandos.
- Cada diretório em `~/mysrc/` é um projeto/repo git. Virtualenvs Python ficam
  em `~/.virtualenvs/`.
