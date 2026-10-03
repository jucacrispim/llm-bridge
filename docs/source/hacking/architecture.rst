Architecture
============

This page describes how the bridge is structured, how the main loop works and
how the pieces fit together. See :doc:`/protocol` for the wire format and
:doc:`providers` for the LLM providers.

Package layout
--------------

The bridge is organized into focused packages:

``cmd/bridge``
    The executable entry point. Parses the CLI flags (``--provider``,
    ``--model``, ``--thinking``, ``--reasoning-effort``, ``-prune``,
    ``-populate-project-kb``, etc.), builds the provider registry, wires the
    knowledge base and logging, and calls ``server.RunWithKnowledgeBase``.
    Also implements the standalone ``-populate-project-kb`` mode.

``server``
    The core. Contains the ``state`` struct, ``handleLine`` (one command per
    line), ``runToolCycle`` (the tool-calling loop), ``cancelTurn``, the
    ``run`` entry point and the helpers that mutate the shared conversation
    state. Holds the embedded default system prompt (``system_prompt.md`` via
    ``//go:embed``).

``llm``
    Provider abstraction. Defines the ``LLMProvider`` interface (``Chat``,
    ``Name``, ``Model``) and the ``ChatRequest``/``ChatResponse``/``Message``/
    ``ToolCall`` types. Implements the **DeepSeek** and **Google/Gemini**
    providers (see :doc:`providers`).

``protocol``
    Wire types and helpers. ``InboundCommand`` (the client → bridge messages)
    and the outbound events (``NewChunk``, ``NewThinking``, ``NewToolCall``,
    ``NewTurnEnd``, ``NewError``, ``NewCancelled``, ``NewUsageDelta``,
    ``NewFilesChanged``, ``NewHook``, ``NewReady``). See :doc:`/protocol`.

``tools``
    ``tools.All()`` returns the tool definitions the bridge exposes to the
    LLM: ``read``, ``write``, ``shell``, ``grep``, ``glob``, ``search_replace``
    and ``knowledge``.

``hooks``
    Resolves and executes ``#``-prefixed hook scripts (project dir first, then
    the general ``~/.llm-bridge/hooks/``). See :doc:`hooks-internals`.

``knowledge``
    The per-project knowledge base: index, ONNX embedder (``Embedder``,
    ``LazyEmbedder``, ``DisabledEmbedder``), the ``Manager`` and the seed
    handling. See :doc:`knowledge-internals`.

``context``
    Loads the Markdown context files (``~/.llm-bridge/*.md`` plus
    ``<cwd>/.llm-bridge/*.md``) that are injected on the first turn. See
    :doc:`context-internals`.

``history``
    Helpers to sanitize/manage the conversation history slice.

``logger``
    The leveled logger used across the bridge (logs go to a file, never to
    stdout, keeping the protocol clean).

Entry points
------------

The public API of the ``server`` package is a set of ``Run*`` functions, all of
which funnel into the single ``run`` function:

- ``Run(r, w, provider)`` — a single fixed provider (legacy/testing).
- ``RunWithProviders(r, w, providers, defaultName)`` — a named provider
  registry, switchable per request.
- ``RunWithSystemPrompt(r, w, providers, defaultName, systemPrompt)`` — adds a
  global system prompt.
- ``RunWithOptions(...)`` — adds the prune flags.
- ``RunWithKnowledgeBase(...)`` — adds the knowledge base (base dir +
  embedder). This is what ``cmd/bridge/main.go`` calls.

Every variant reads from ``r`` and writes to ``w``, so the bridge is easily
testable with in-memory readers/writers.

The ``state`` struct
--------------------

``server.state`` is the single in-memory object holding all conversation
state: the active provider and overrides, the history, the pending tool call
IDs/results, the current cwd, the knowledge base manager, token accounting,
the cancel function, and the mutexes that protect the concurrent pieces.

Because a streaming ``Chat`` and the reader goroutine access shared state
concurrently, the state uses three mutexes:

``cancelMut``
    Guards the ``cancel`` field, touched by the reader goroutine (``cancel``
    command) and by ``runToolCycle`` while a ``Chat`` streams.

``writeMut``
    Serializes all writes to the bridge's stdout, so a ``cancelled`` event
    from the reader goroutine never interleaves with the chunks the provider's
    streaming callbacks are writing.

``historyMut``
    Guards the ``history`` slice, accessed concurrently by ``cancelTurn``
    (via ``history.Sanitize``) and by ``runToolCycle`` (appending the
    assistant message once a cancelled ``Chat`` returns). All reads/writes of
    history go through the locked helpers (``appendUser``, ``appendAssistant``,
    ``appendToolResult``, ``historySnapshot``, ``historyLen``, ``sanitizeHistory``).

Main loop and the reader goroutine
----------------------------------

``run`` starts a **reader goroutine** that keeps consuming stdin even while the
main loop is blocked inside a streaming ``Chat``:

- A line whose method is ``cancel`` is handled *right in the reader
  goroutine*: it calls ``cancelTurn`` and writes ``cancelled`` immediately, so
  an in-flight request is interrupted without waiting for the model to stop.
- Every other line is forwarded over a buffered channel (``cmdCh``) to the
  main loop, which processes commands serially via ``handleLine``.

The scanner is configured with an 8 MB per-line limit, because a single command
line can be large (e.g. ``set_knowledge_bases`` metadata). The default 64 KB
limit would otherwise kill the whole bridge on a big line.

The request flow
----------------

#. ``handleLine`` receives a ``prompt`` command, applies any per-request
   overrides (provider/model/thinking/reasoning-effort/system), records
   ``historyLenBeforeTurn`` and appends the user message.
#. It calls ``runToolCycle``, the heart of the bridge.
#. ``runToolCycle`` loops calling the active provider's ``Chat`` with the
   current history snapshot, all tools and the effective system prompt. It
   publishes the cancel function so the reader goroutine can interrupt the
   stream, then relays streaming ``chunk``/``thinking`` events to the client.
#. If the model returned tool calls, the bridge classifies each one. **Read-only**
   tools (``read``/``grep``/``glob``, plus the fully internal ``knowledge``) are
   run by the bridge right away — the result is appended to the history and a
   ``tool_call`` event is emitted for display only. **Mutating** tools
   (``shell``/``write``/``search_replace``) are emitted as ``tool_confirm``
   events and queued in ``pendingApprovals``; the loop returns, waiting for the
   client to approve each one.
#. When a ``tool_confirm`` approval arrives, ``handleLine`` runs that tool
   (``executeTool``), appends its result and — once ``pendingApprovals`` is empty
   — calls ``runToolCycle`` again. The loop repeats until the model produces a
   final answer with no tool calls. A denied tool is signalled with ``cancel``
   (which aborts the turn).
#. On a final answer, the bridge emits ``turn_end`` (with stop reason, model
   and token counts), a ``files_changed`` event listing any paths written this
   turn, and a final ``usage_delta``. It then runs the optional pruning
   (``collapseTurn`` / ``pruneTurn``) and ``sanitizeHistory``, resets the
   pending-tool state and returns.

Cancellation
------------

Cancellation is defensive: ``cancelTurn`` cancels the in-flight ``Chat``
context, marks the turn cancelled, and sanitizes the history. Because the
reader goroutine handles ``cancel`` immediately, the user does not have to wait
for the model to finish talking.

If ``Chat`` returns ``context.Canceled``, the partial thinking/content that was
streamed before the interruption is still preserved in the history (so the
cancelled turn's context is kept for the next prompt), and a stale
``tool_confirm`` approval (or legacy ``tool_result``) from a cancelled turn is
silently ignored instead of erroring.

Hooks
-----

A ``prompt`` whose text starts with ``#`` is treated as a hook, not sent to the
LLM. ``handleLine`` parses the hook name/args and launches ``hooks.Run`` in a
goroutine, returning immediately so the main loop keeps processing. The
result is written as a single ``hook_action`` event through ``st.write``
(serialized on ``writeMut``), so it never interleaves with streaming. Hooks do
not touch the history and are not cancellable. See :doc:`hooks-internals`.

Pruning and history management
------------------------------

To save tokens and keep the history prefix byte-stable for provider caching:

- ``-prune`` collapses reasoning and tool results of file tools into user
  snapshots (read/write/replace), preserving file state without storing every
  tool call or its chain-of-thought.
- ``-aggressive-prune`` collapses each completed tool-calling turn into just
  the user prompt + the final answer, dropping intermediate tool calls, tool
  results and chain-of-thought entirely.

Both are mutually exclusive and off by default. See :doc:`/usage`.

Knowledge base
--------------

On ``set_cwd``, the server builds the per-project ``knowledge.Manager`` lazily
from ``kbBaseDir/<project>/data.json`` (clearing it when disabled). The
``knowledge`` tool calls are resolved locally inside ``runToolCycle`` so they
never wait on the client. A one-line note about the project's KB is injected on
the first turn when the KB is enabled. See :doc:`knowledge-internals`.
