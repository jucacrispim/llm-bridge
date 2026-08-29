Overview
========

**llm-bridge** is a bridge written in Go that mediates between a client (such
as Emacs) and LLM providers. It reads messages from the client as
JSON-lines on **stdin**, forwards the conversation to the appropriate
provider, and writes the provider's streamed response back as JSON-lines on
**stdout**. Logging goes to a file (``-logfile``) so the stdout protocol stays
clean for the client.

Why a bridge?
-------------

Instead of the client talking directly to an LLM provider, the bridge sits in
between and adds capabilities that a plain API client does not have:

- **Local tools**: the model can ``read``, ``write``, ``search_replace``,
  ``grep``, ``glob`` and run ``shell`` commands on the host — turning a chat
  client into a coding assistant that actually works on the project on disk.
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
   ``chunk`` (content), ``thinking`` (chain-of-thought), ``tool_call``,
   ``usage_delta``, and finally ``turn_end``.
#. If the model requests a tool, the bridge emits a ``tool_call`` event and
   waits for the client to run the tool and reply with a ``tool_result``. The
   tool loop (``runToolCycle``) keeps the tool calls and results in the
   history and repeats until the model produces a final answer.
#. When the model finishes, the turn ends. If the tool loop wrote files via
   the ``write``/``search_replace`` tools, the paths are reported to the client
   as a ``files_changed`` event (used, for example, to trigger automated
   tests).

Because the protocol is line-based, the bridge stays simple: a main loop reads
one command per line and writes one event per line. Long-running work — a
streaming chat or a hook script — runs off the main loop so the bridge keeps
responding to ``cancel``, ``tool_result`` and new prompts.

Where it runs and how the client connects
-----------------------------------------

The bridge is a single binary that runs on the host, typically launched by the
client as a subprocess. There is no network port by default: the client spawns
``./build/llm-bridge [flags]`` and communicates over its stdin/stdout pipes.
Project-local files (the ``<cwd>`` directory) are therefore directly
accessible to the model's tools.

The protocol itself is described in :doc:`protocol`, the providers in
:doc:`providers`, hooks in :doc:`hooks`, the knowledge base in
:doc:`knowledge`, context loading in :doc:`context`, and build/run details in
:doc:`usage`.
