Providers
=========

The bridge supports two LLM providers: **DeepSeek** and **Google/Gemini**.
Both implement the same ``llm.LLMProvider`` interface:

.. code-block:: go

   type LLMProvider interface {
       Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
       Name() string
       Model() string
   }

The active provider is selected at startup with ``--provider``
(``deepseek`` or ``google``). Because both providers are registered up front,
a single prompt can switch the active provider (and its model, thinking mode,
reasoning effort or system prompt) through the per-request overrides described
in :doc:`/protocol` — the override persists across subsequent prompts.

The shared request/response types (``llm/interface.go``) are:

- ``ChatRequest`` — messages, tools, optional ``System``/``Model``, and the
  ``Thinking`` / ``ReasoningEffort`` overrides plus an ``OnReasoning`` callback
  invoked with each streamed chain-of-thought fragment.
- ``ChatResponse`` — ``Content``, ``StopReason``, ``Usage``, ``ToolCalls``,
  the actual ``Model`` used and the full ``Reasoning``.
- ``Message`` — a history entry (role, content, optional tool calls / tool
  call id, and the ``Reasoning`` chain-of-thought that accompanied it).
- ``ToolCall`` — id, name, raw JSON arguments and, for Gemini, the
  ``ThoughtSignature`` that must be echoed back.

Common provider selection logic
-------------------------------

Both providers honor, in order of priority:

#. An explicit **per-request model** (from a prompt's ``model`` override).
#. An explicitly **configured provider model** (``--model`` when it is the
   default provider, or ``DEEPSEEK_MODEL`` / ``GOOGLE_MODEL``).

DeepSeek adds a third step: when no explicit model is configured anywhere, the
model is **derived from the thinking mode** — ``deepseek-flash`` when
thinking is on, ``deepseek-chat`` when off (``llm.DefaultModel``).

Thinking and reasoning effort are also resolved per request through the same
pattern (``effectiveThinking`` / ``effectiveReasoningEffort``): a per-request
override wins over the provider's configured value.

DeepSeek
--------

Implemented in ``llm/deepseek.go``. The provider uses the OpenAI-compatible
``/chat/completions`` endpoint with streaming.

Model resolution (``resolveModel``)
    Per-request model → explicit provider model (``--model`` /
    ``DEEPSEEK_MODEL``) → default derived from the effective thinking mode
    (``deepseek-flash`` / ``deepseek-chat``).

Thinking (``ThinkingOptions``)
    Thinking mode is sent explicitly through the structured ``thinking``
    parameter accepted by DeepSeek v3/v4 models:

    - **on**  → ``{"thinking": {"type": "enabled"}}`` plus the top-level
      ``reasoning_effort`` (default ``"high"``) that controls the strength of
      the reasoning stage.
    - **off** → ``{"thinking": {"type": "disabled"}}``. This is required even
      for models that reason *by default* (e.g. ``deepseek-v4-flash``) — simply
      omitting ``reasoning_effort`` is not enough to stop them.

Reasoning effort (``effectiveReasoningEffort``)
    Sent as ``reasoning_effort`` when thinking is on (e.g. ``low`` /
    ``medium`` / ``high``; default ``high``). An empty value disables sending
    the parameter. A per-request override wins over the provider's configured
    value.

Reasoning content
    DeepSeek streams the chain-of-thought in ``reasoning_content``, which the
    provider separates from the visible content and surfaces through
    ``OnReasoning``.

    Whether that reasoning stays in the conversation history depends on the
    flags:

    - **Default mode (no pruning)** — the bridge keeps the full history,
      including each assistant message's chain-of-thought and every tool call
      and result, and sends it back to the API on each request. For DeepSeek,
      the assistant's ``reasoning_content`` is sent back to the API. **only**
      when that message performed tool calls (DeepSeek's thinking mode returns
      HTTP 400 otherwise); for final answers without tool calls it is omitted
      on the wire to save tokens. This is a per-request omission only — it
      does **not** clean the history, which keeps everything.
    - **With ``-prune`` or ``-aggressive-prune``** — the chain-of-thought (and,
      for ``-aggressive-prune``, the intermediate tool calls and tool results
      too) is actually **removed from the stored history** once the turn
      completes. See :doc:`/usage` for the difference between the two modes.

Environment variables
    ``DEEPSEEK_API_KEY`` (required), ``DEEPSEEK_URL`` (default
    ``https://api.deepseek.com/chat/completions``), ``DEEPSEEK_MODEL``
    (explicit model), ``DEEPSEEK_THINKING`` (default ``true``) and
    ``DEEPSEEK_REASONING_EFFORT`` (default ``"high"``).

Google / Gemini
---------------

Implemented in ``llm/google.go``. The provider calls Gemini's
``:streamGenerateContent?alt=sse`` endpoint.

Model resolution (``resolveModel``)
    Per-request model → provider model (``--model`` when google is the default,
    then ``GOOGLE_MODEL`` / ``GEMINI_MODEL``, then the default
    ``gemini-2.5-flash``). Unlike DeepSeek, the model is **not** derived from
    the thinking mode.

Request shape (``googleRequest``)
    Messages are grouped into ``contents`` with roles ``user`` / ``model``.
    System instructions are gathered into the top-level
    ``system_instruction`` field (Gemini has no lone system message in
    ``contents``). Tools are sent as ``functionDeclarations``.

Thinking budget (``effectiveThinkingBudget``)
    Thinking maps onto Gemini's ``generationConfig.thinkingConfig.thinkingBudget``:

    - **off** → budget ``0`` (disables the model's reasoning stage).
    - **on**  → a positive budget (default ``1024``) allocating that many
      tokens to the reasoning stage. A per-request ``reasoning_effort`` that
      parses as a number wins over the provider's configured budget.

Function calls and ``thought_signature``
    Gemini emits a function call as a part carrying ``functionCall``
    (``name`` + ``args`` object, unlike OpenAI's JSON-string ``arguments``),
    plus — when thinking is on — a **sibling** ``thoughtSignature`` field. The
    provider preserves it in ``ToolCall.ThoughtSignature`` so it can be echoed
    back on the same part when re-sending history. Tool results are matched by
    **name**, so the call id *is* the function name.

Tool results as objects
    Gemini requires ``functionResponse.response`` to be a JSON **object**. The
    ``toolResultAsJSON`` helper passes an object through as-is and wraps
    anything else (string/number/array/bool, or invalid JSON) in
    ``{"result": ...}`` to avoid HTTP 400.

Environment variables
    ``GOOGLE_API_KEY`` (required; ``GEMINI_API_KEY`` also honored),
    ``GOOGLE_URL`` (default ``https://generativelanguage.googleapis.com/v1beta``),
    ``GOOGLE_MODEL`` / ``GEMINI_MODEL`` (default ``gemini-2.5-flash``),
    ``GOOGLE_THINKING`` (default ``true``) and ``GOOGLE_THINKING_BUDGET``
    (default ``1024``).
