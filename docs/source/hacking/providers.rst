Providers
=========

The bridge supports three LLM providers: **DeepSeek**, **Google/Gemini** and
**Anthropic/Claude**. All implement the same ``llm.LLMProvider`` interface:

.. code-block:: go

   type LLMProvider interface {
       Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
       Name() string
       Model() string
   }

The active provider is selected at startup with ``--provider``
(``deepseek``, ``google`` or ``anthropic``). Because all providers are
registered up front, a single prompt can switch the active provider (and its
model, thinking mode, reasoning effort or system prompt) through the per-request
overrides described
in :doc:`/protocol`. An override persists across subsequent prompts.

The shared request/response types (``llm/interface.go``) are:

- ``ChatRequest`` - messages, tools, optional ``System``/``Model``, the
  ``Thinking`` / ``ReasoningLevel`` / ``ThinkingBudget`` overrides and an
  ``OnReasoning`` callback invoked with each streamed chain-of-thought
  fragment.
- ``ChatResponse`` - ``Content``, ``StopReason``, ``Usage``, ``ToolCalls``,
  the actual ``Model`` used, the full ``Reasoning`` and its ``Signature``.
- ``Message`` - a history entry: role, content, optional tool calls and tool
  call id, the ``Reasoning`` that accompanied it and its ``Signature``.
- ``ToolCall`` - id, name, raw JSON arguments and, for Gemini, the
  ``Signature`` that must be echoed back.

Nothing in those types is the wire shape of one provider. Each provider keeps
its own private structs (``openAIRequest``, ``googleRequest``, and so on) and
converts at the edge.

Effort, signatures and usage
----------------------------

Three things differ enough between providers that the interface states them
once, in provider-neutral words:

``ReasoningLevel`` / ``ThinkingBudget``
    The effort is asked for either as a level (``low``, ``medium``, ``high``,
    ``xhigh``, ``max``) or as a token budget. A provider maps whichever it
    understands and ignores the other: DeepSeek sends the level as
    ``reasoning_effort``, Gemini turns it into a ``thinkingBudget``, Anthropic
    sends the level as ``output_config.effort`` and the budget as
    ``thinking.budget_tokens``. ``protocol`` still takes one string and the
    server splits it (``parseReasoningEffort``): a word becomes a level and a
    number becomes a budget.

``Signature``
    Some providers prove the chain-of-thought came from them and refuse a
    replayed history without the proof. Gemini signs the function call, so the
    proof lives in ``ToolCall.Signature``. Anthropic signs the thinking block,
    so it lives in ``Message.Signature``. DeepSeek signs nothing. The bridge
    stores it next to the reasoning it belongs to and echoes it back unchanged.

``Usage.CachedTokens``
    The one cache counter: prompt tokens a provider served from its cache, with
    the uncached remainder being ``PromptTokens - CachedTokens``. DeepSeek
    reports hit and miss separately and Gemini reports one cached total; each
    conversion happens in its own file. The server derives the two numbers
    ``turn_end`` carries.

``StopReason``
    ``end_turn``, ``tool_use`` and ``max_tokens`` are the common three, and a
    value outside them goes out as the provider wrote it (``SAFETY``, for
    instance), because the client would rather know which one it was.

Common provider selection logic
-------------------------------

Both providers honor, in order of priority:

#. An explicit **per-request model** (from a prompt's ``model`` override).
#. An explicitly **configured provider model** (``--model`` when it is the
   default provider, or ``DEEPSEEK_MODEL`` / ``GOOGLE_MODEL``).

DeepSeek adds a third step: when no explicit model is configured anywhere, the
model is **derived from the thinking mode**: ``deepseek-flash`` when
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
      for models that reason *by default* (e.g. ``deepseek-v4-flash``).
      Omitting ``reasoning_effort`` alone does not stop them.

Reasoning effort (``effectiveReasoningEffort``)
    Sent as ``reasoning_effort`` when thinking is on (default ``high``). A level
    above what DeepSeek knows (``xhigh``, ``max``) goes out as ``high``, because
    the API lists three values and would reject the other two. A token budget is
    ignored here: DeepSeek takes an effort name, not a number. An empty level
    stops sending the parameter, and a per-request override wins over the
    provider's configured value.

Reasoning content
    DeepSeek streams the chain-of-thought in ``reasoning_content``, which the
    provider separates from the visible content and surfaces through
    ``OnReasoning``.

    Whether that reasoning stays in the conversation history depends on the
    flags:

    - **Default mode (no pruning)** - the bridge keeps the full history,
      including each assistant message's chain-of-thought and every tool call
      and result, and sends it back to the API on each request. For DeepSeek,
      the assistant's ``reasoning_content`` goes back to the API **only** when
      that message performed tool calls, because the thinking mode answers
      HTTP 400 otherwise. A final answer without tool calls omits it on the
      wire, to save tokens. That is a per-request omission, not a clean: the
      history keeps everything.
    - **With ``-prune`` or ``-aggressive-prune``** - the chain-of-thought (and,
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
    ``gemini-3-flash``). Unlike DeepSeek, the model is **not** derived from
    the thinking mode.

Request shape (``googleRequest``)
    Messages are grouped into ``contents`` with roles ``user`` / ``model``.
    System instructions are gathered into the top-level
    ``system_instruction`` field (Gemini has no lone system message in
    ``contents``). Tools are sent as ``functionDeclarations``.

Thinking budget (``effectiveThinkingBudget``)
    Thinking maps onto Gemini's ``generationConfig.thinkingConfig.thinkingBudget``:

    - **off** → budget ``0`` (disables the model's reasoning stage).
    - **on**  → a positive budget allocating that many tokens to the reasoning
      stage. A per-request token budget wins over the provider's configured
      budget; a per-request level is turned into one by ``googleLevelBudgets``
      (``low`` 1024, ``medium`` 4096, ``high`` 8192, ``xhigh`` 16384, ``max``
      32768).

Function calls and ``thought_signature``
    Gemini emits a function call as a part carrying ``functionCall``
    (``name`` + ``args`` object, unlike OpenAI's JSON-string ``arguments``),
    plus, when thinking is on, a **sibling** ``thoughtSignature`` field. The
    provider preserves it in ``ToolCall.Signature`` so it can be echoed
    back on the same part when re-sending history. Tool results are matched by
    **name**, so the call id *is* the function name.

Tool results as objects
    Gemini requires ``functionResponse.response`` to be a JSON **object**. The
    ``toolResultAsJSON`` helper passes an object through as-is and wraps
    anything else (string/number/array/bool, or invalid JSON) in
    ``{"result": ...}`` to avoid HTTP 400.

Environment variables
    ``GOOGLE_API_KEY`` (required, and ``GEMINI_API_KEY`` is honored too),
    ``GOOGLE_URL`` (default ``https://generativelanguage.googleapis.com/v1beta``),
    ``GOOGLE_MODEL`` / ``GEMINI_MODEL`` (default ``gemini-3-flash``),
    ``GOOGLE_THINKING`` (default ``true``) and ``GOOGLE_THINKING_BUDGET``
    (default ``1024``).

Anthropic / Claude
------------------

Implemented in ``llm/anthropic.go``. The provider calls ``POST /v1/messages``
with ``stream: true`` and sends the API key in ``x-api-key`` plus the fixed
``anthropic-version`` header (``2026-10-21`` unless ``ANTHROPIC_VERSION`` says
otherwise).

Model and ``max_tokens``
    Per-request model → provider model (``--model`` when anthropic is the
    default, then ``ANTHROPIC_MODEL``, then ``claude-sonnet-5-5``).
    ``max_tokens`` is required by the API and defaults to ``8192``
    (``ANTHROPIC_MAX_TOKENS``). The provider raises it above the reasoning
    budget when the budget would not fit under it.

Thinking (``effectiveThinkingConfig``)
    Three shapes, and which one you get depends on whether a budget was asked
    for:

    - **off** → ``{"type": "disabled"}``.
    - **on, no budget** → ``{"type": "adaptive", "display": "summarized"}``, so
      the model decides how much to think.
    - **on, with a budget** → ``{"type": "enabled", "budget_tokens": N,
      "display": "summarized"}``. The API takes 1024 at the lowest, and the
      budget has to stay under ``max_tokens``.

Effort (``effectiveEffort``)
    Sent as ``output_config.effort``. Anthropic knows the whole common scale
    (``low``, ``medium``, ``high``, ``xhigh``, ``max``), so the level goes out
    as it came.

Messages are blocks
    An assistant message is a list of content blocks: the ``thinking`` block
    first (with its signature), then the text, then one ``tool_use`` per call.
    A tool result is not a message of its own: it is a ``tool_result`` block
    inside a user message, and the consecutive results of one assistant message
    share a single user message. Images go as ``image`` blocks, either
    ``source.type: "base64"`` with the bytes or ``source.type: "url"``.

Thinking signatures
    Claude signs the thinking block. The signature comes back in a
    ``signature_delta`` event, is kept in ``Message.Signature``, and has to go
    back unmodified and in the original position, or the API answers 400 with
    ``invalid_request_error``.

Usage
    ``input_tokens``, ``cache_read_input_tokens`` and
    ``cache_creation_input_tokens`` are all prompt tokens, so all three are
    summed into ``PromptTokens``. ``CachedTokens`` is the cache read part.

Environment variables
    ``ANTHROPIC_API_KEY`` (required), ``ANTHROPIC_URL`` (default
    ``https://api.anthropic.com/v1/messages``), ``ANTHROPIC_VERSION``,
    ``ANTHROPIC_MODEL``, ``ANTHROPIC_MAX_TOKENS``, ``ANTHROPIC_THINKING``
    (default ``true``) and ``ANTHROPIC_THINKING_BUDGET``. A budget selects
    manual extended thinking; leaving it unset keeps adaptive thinking.

.. _providers-context-window:

Context window
--------------

Providers do not expose a model's context window over the API, so the bridge
keeps a static per-model table in ``llm/contextwindow.go``. The ``turn_end``
event reports ``context_tokens`` (the prompt of the last provider call of the
turn), ``context_window`` and their ratio ``context_pct``. Lookup is exact
first, then by the **longest matching prefix**, so versioned ids such as
``gemini-2.5-flash-001`` resolve to their base entry. A model absent from the
table reports ``context_pct: null`` and ``context_window: 0`` instead of a
guessed window.

.. list-table::
   :header-rows: 1
   :widths: 60 40

   * - Model id (or prefix)
     - Context window
   * - ``deepseek-flash``, ``deepseek-chat``, ``deepseek-reasoner``
     - 1,000,000
   * - ``deepseek-v4-flash``, ``deep-v4-pro``
     - 1,000,000
   * - ``gemini-3.8-flash``, ``gemini-3.1-pro``
     - 1,000,000
   * - ``gemini-3-flash``
     - 200,000
   * - ``gemini-2.5-pro``, ``gemini-2.5-flash``, ``gemini-1.5-flash``
     - 1,000,000
   * - ``gemini-1.5-pro``
     - 2,000,000
   * - ``gemma-4``
     - 128,000
   * - ``claude-sonnet-5-5``, ``claude-opus-5-5``, ``claude-haiku-5-5``
     - 1,000,000
   * - ``claude-opus-4-6``, ``claude-sonnet-4-6``, ``claude-opus-4-7``, ``claude-opus-4-8``
     - 1,000,000
   * - ``claude-sonnet-4-5``, ``claude-opus-4-5``, ``claude-haiku-4-5``
     - 200,000
