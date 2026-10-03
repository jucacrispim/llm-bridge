Overview
========

**llm-bridge** is a bridge written in Go that mediates between a client (such
as Emacs) and LLM providers. It reads messages from the client as
JSON-lines on **stdin**, forwards the conversation to the appropriate
provider, and writes the provider's streamed response back as JSON-lines on
**stdout**. Logging goes to a file (``-logfile``) so the stdout protocol stays
clean for the client.

Features
--------

Instead of the client talking directly to an LLM provider, the bridge sits in
between and gives the client a single, stable surface that a plain provider SDK
does not:

- **Unified API across providers**: one JSON-lines protocol for every provider.
  ``prompt``, ``tool_confirm``, ``cancel`` and the rest mean the same thing no
  matter whether the active provider is **DeepSeek** or **Google/Gemini**; the
  bridge translates the request/response format, streaming style,
  authentication and thinking mode for each one.
- **Tool calls executed by the bridge**: the models already know how to call
  tools, so the bridge advertises the tool schemas *and runs the tools itself* —
  ``read``, ``write``, ``search_replace``, ``grep``, ``glob`` and ``shell`` —
  directly on the host and the project on disk, turning a chat client into a
  coding assistant. Read-only tools run without confirmation; the mutating ones
  (``shell`` / ``write`` / ``search_replace``) are offered to the client for
  approval via a ``tool_confirm`` event before the bridge runs them.
- **Image support (multimodal)**: a prompt can carry images by ``path`` (read
  by the bridge), ``url`` (passed through to the provider) or ``data`` (inline
  base64, e.g. pasted into the chat). The bridge normalizes them to each
  provider's format — an OpenAI ``image_url`` content part for DeepSeek, an
  inline or ``fileData`` part for Gemini.
- **Provider switching per request**: both **DeepSeek** and **Google/Gemini**
  providers are registered up front, so a single prompt can switch the active
  provider (and model, thinking mode, reasoning effort or system prompt) via
  overrides that persist between prompts.
- **Hooks**: messages starting with ``#`` run a local script instead of
  calling the LLM, useful for project-specific commands.
- **Project context and knowledge base**: project/global Markdown context is
  injected into the conversation, and an optional per-project knowledge base
  (with an ONNX embedder) can be queried through a ``knowledge`` tool.
- **History management**: tool-calling turns can be pruned/collapsed
  (``-prune`` / ``-aggressive-prune``) to save tokens and keep the prefix
  cacheable.

Basic conversation flow
-----------------------

#. The client starts the bridge and sends a ``prompt`` command on stdin.
#. The bridge appends the message to the conversation history and, on the
   first turn, injects the loaded context files (and a one-line note about the
   project knowledge base when enabled).
#. It calls the active provider (default from ``--provider``, switchable via
   the ``provider`` override).
#. The provider's streamed output is relayed back to the client as events:
   ``chunk`` (content), ``thinking`` (chain-of-thought), ``tool_call`` (a
   read-only tool, already run by the bridge), ``tool_confirm`` (a mutating tool
   awaiting approval), ``usage_delta``, and finally ``turn_end``.
#. If the model requests a tool, the bridge runs the read-only ones immediately
   and, for the mutating ones, emits a ``tool_confirm`` event and waits for the
   client's approval before running them. The tool loop (``runToolCycle``) keeps
   the tool calls and results in the history and repeats until the model
   produces a final answer.
#. When the model finishes, the turn ends. If the tool loop wrote files via
   the ``write``/``search_replace`` tools, the paths are reported to the client
   as a ``files_changed`` event (used, for example, to trigger automated
   tests).

Because the protocol is line-based, the bridge stays simple: a main loop reads
one command per line and writes one event per line. Long-running work — a
streaming chat or a hook script — runs off the main loop so the bridge keeps
responding to ``cancel``, ``tool_confirm`` and new prompts.

Where it runs and how the client connects
-----------------------------------------

The bridge is a single binary that runs on the host, typically launched by the
client as a subprocess. There is no network port by default: the client spawns
``./build/llm-bridge [flags]`` and communicates over its stdin/stdout pipes.
Project-local files (the ``<cwd>`` directory) are therefore directly
accessible to the model's tools.

The protocol itself is described in :doc:`protocol`, tool calls in
:doc:`tools`, the providers in :doc:`/hacking/providers`, hooks in
:doc:`hooks`, the knowledge base in :doc:`knowledge`, context loading in
:doc:`context`, and build/run details in :doc:`usage`.
