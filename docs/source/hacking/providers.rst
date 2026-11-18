Providers
=========

The bridge supports four LLM providers: **DeepSeek**, **Google/Gemini**,
**Anthropic/Claude** and **OpenAI**. All implement the same ``llm.LLMProvider`` interface:

.. code-block:: go

   type LLMProvider interface {
       Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
       Name() string
       Model() string
   }

The active provider is selected at startup with ``--provider``
(``deepseek``, ``google``, ``anthropic`` or ``openai``). Because all providers are
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

Prompt caching
    Three ``cache_control`` breakpoints go out on every request: the last tool
    of the tool list, the system prompt, and the last block of the last
    message. The cache is a prefix, so the tool breakpoint covers everything up
    to it and the system breakpoint covers both. The API accepts four.

    ``system`` is a block list rather than the bare string the API also takes,
    because a breakpoint has nowhere to go on a string. The message breakpoint
    walks back over ``thinking`` blocks: the signature is the fragile part of a
    replayed block, and the breakpoint reads better on the text before it.

    Inside a turn the history only grows, so the message breakpoint hits across
    the tool calls of that turn. It also hits across turns, unless you run
    ``-prune`` or ``-aggressive-prune``: both rewrite the middle of the history,
    so the first request after a pruned turn writes the cache again and the
    following ones hit. The tool and system breakpoints sit at the front of the
    prefix and are not affected.

    The API caches a prefix only above a minimum length, 1024 tokens for the
    current models. Below it the breakpoint is ignored, not rejected. Cache
    reads come back in ``cache_read_input_tokens`` and reach ``turn_end`` as
    ``cache_hit_tokens``.

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

OpenAI
------

Implemented in ``llm/openai.go``. The provider calls ``POST /v1/responses``
with ``stream: true`` and sends the API key in ``Authorization: Bearer``.

The Responses API is not a stylistic choice. ``/v1/chat/completions`` is not
enough for the current models: they take tool calls on the Responses API only,
and the bridge lives on tool calls.

Model
    Per-request model → provider model (``--model`` when openai is the
    default, then ``OPENAI_MODEL``, then ``gpt-6.2-sol``).

``store: false``
    Sent on every request, so the response is not kept on the OpenAI side. A
    reasoning item then comes back only encrypted, which is why the request
    also carries ``include: ["reasoning.encrypted_content"]``.

Input is a flat item list
    ``input`` is not a list of messages. A user turn is a message item, a tool
    call is a ``function_call`` item of its own, and a tool result is a
    ``function_call_output`` item. The system prompt does not go in the list at
    all: it is the top level ``instructions`` field.

The reasoning item
    A tool-calling turn comes back with a ``reasoning`` item. The API wants it
    back with the calls it led to, before them, or the calls lose their
    context. The bridge keeps the item as it came, encrypted content included,
    in ``Message.Signature``, and replays it first in the assistant turn.
    Anthropic uses the same field for a different thing, the signature of the
    thinking block.

Thinking (``effectiveThinking``)
    Thinking off means the ``reasoning`` parameter is left out of the request.
    ``effort: "none"`` would be the explicit off switch, and the current models
    reject it, so there is no explicit off switch to send.

Effort
    Sent as ``reasoning.effort``. OpenAI knows the whole common scale
    (``low``, ``medium``, ``high``, ``xhigh``, ``max``), default ``medium``.
    There is no numeric budget, so ``thinking_budget`` is ignored.

Reasoning summary
    ``reasoning.summary: "auto"`` makes the reasoning text come back, which is
    what reaches your client as ``thinking``. It stays off unless
    ``OPENAI_REASONING_SUMMARY`` says otherwise: the summarizers can require
    organization verification, and a request that asks for a summary without it
    fails whole.

Tools
    ``{"type": "function", "name", "description", "parameters"}``. ``strict``
    is left out: it demands ``additionalProperties: false`` and every property
    in ``required``, and the bridge tool schemas do not follow that.

Images
    ``input_image`` parts with a ``data:`` URL for an inline image or an
    ``http(s)`` URL for a remote one.

Usage
    ``input_tokens`` includes the cached prompt, and
    ``input_tokens_details.cached_tokens`` is the cached part.
    ``output_tokens`` includes the reasoning.

Environment variables
    ``OPENAI_API_KEY`` (required), ``OPENAI_URL`` (default
    ``https://api.openai.com/v1/responses``), ``OPENAI_MODEL``,
    ``OPENAI_THINKING`` (default ``true``), ``OPENAI_REASONING_EFFORT``
    (default ``medium``), ``OPENAI_REASONING_SUMMARY`` (unset keeps the summary
    off) and ``OPENAI_MAX_OUTPUT_TOKENS`` (unset lets the API decide).

.. _providers-context-window:

Context window
--------------

Providers do not expose a model's context window over the API, so the bridge
reads it from ``llm/models.json``, embedded in the binary. The ``turn_end``
event reports ``context_tokens`` (the prompt of the last provider call of the
turn), ``context_window`` and their ratio ``context_pct``. Lookup is exact
first, then by the **longest matching prefix**, so versioned ids such as
``gemini-2.5-flash-001`` resolve to their base entry. A model absent from the
file reports ``context_pct: null`` and ``context_window: 0`` instead of a
guessed window.

The ``ready`` event announces the same file: each provider carries the ids it
answers for, with the window, and the reasoning controls it takes.

.. code-block:: json

   {
     "providers": {
       "deepseek": {
         "levels": ["low", "medium", "high"],
         "budget": false,
         "models": [
           {"id": "deepseek-flash", "window": 1000000},
           {"id": "deepseek-chat", "window": 1000000, "alias": "deepseek-flash"}
         ]
       }
     }
   }

* ``levels`` - Reasoning levels the provider accepts, ascending on the scale
  ``low``, ``medium``, ``high``, ``xhigh``, ``max``.
* ``budget`` - Whether a numeric token budget works instead of a level.
* ``models`` - Announced in file order, so put the newest first.
* ``window`` - Token window of the model. Must be above zero.
* ``alias`` - The model this id redirects to. An alias keeps its window, since
  a request may still name it, and is not announced.
* ``price`` - Optional. USD per million tokens. An alias without a ``price``
  takes the one of its target.

The bridge refuses a file with an unknown level, levels out of order, a
missing id, a zero window, a negative price, an id in two providers or an alias
that does not point at a model of the same provider.

.. _providers-price:

Prices
~~~~~~

The ``turn_end`` event reports the cost of the turn and of the session. Each
provider works the cost out of the ``usage`` of the call, using the ``price``
of the model. Lookup is the same as for the window: exact first, then the
longest prefix. A model without a ``price`` has no cost, and the turn reports
``null``.

.. code-block:: json

   {"id": "claude-haiku-5-5", "window": 1000000,
    "price": {"input": 0.10, "cached": 0.01, "cache_write": 0.125, "output": 0.50,
              "over": {"above": 100000, "input": 0.50, "cached": 0.05,
                       "cache_write": 0.625, "output": 2.50}}}

* ``input`` - Prompt token that did not come from the cache.
* ``cached`` - Prompt token read from the cache.
* ``cache_write`` - Prompt token written to the cache. Defaults to ``input``.
  Only Anthropic bills it apart.
* ``output`` - Generated token. The reasoning is billed as output, so it is in
  here.
* ``over`` - Rates for a prompt above ``above`` tokens, with the same fields.
  Anthropic counts the cache in the prompt. Gemini counts ``promptTokenCount``.
  OpenAI counts ``input_tokens``. Every OpenAI model with a price has a tier
  above 272000 tokens.
* ``off_peak`` - Rates outside the peak hours. The rates around it are then the
  peak ones. Only DeepSeek uses it.

A rate left out is ``0``, so a model that bills a cache read needs ``cached``.

``gpt-6.1-sol`` and ``gpt-6-sol`` are in the catalog without a ``price``, so a
turn on either reports ``cost_usd: null``. Add a ``price`` to the file when you
know the rates.

DeepSeek peak hours are Monday to Friday, 01:00-04:00 and 06:00-10:00 UTC. The
bridge checks the clock when the call ends. Chinese public holidays are
off-peak at DeepSeek, but the bridge has no calendar, so it bills them as peak.

Prices change. Gemini 4 Argon doubles on 2026-12-01 and Gemini 3.8 Flash
doubles on 2027-01-01, so edit the file then.

To change the data without a rebuild, save a file as
``~/.config/llm-bridge/models.json`` (``$XDG_CONFIG_HOME`` is honored). It
replaces the embedded file as a whole, so start from a copy of
``llm/models.json``.

.. code-block:: sh

   $ mkdir -p ~/.config/llm-bridge
   $ cp llm/models.json ~/.config/llm-bridge/models.json

If the file is invalid the bridge logs ``models file ignored`` and keeps the
embedded data. The file is read once at startup.

.. _providers-system-prompts:

System prompts
--------------

Each model can have its own system prompt. The prompts are markdown files in
``server/prompts/``, embedded in the binary. ``default.md`` is the one every
model gets unless a more specific file exists. The bridge picks the file on
each request, so a model override changes the prompt too.

The name of the file is matched in this order:

1. The longest file name that is a prefix of the model id.
2. The name of the provider.
3. ``default``.

So, with these files:

.. code-block:: sh

   $ ls ~/.config/llm-bridge/prompts
   claude.md  claude-opus.md  deepseek.md  default.md

``claude-opus-5-5`` gets ``claude-opus.md``, ``claude-sonnet-5`` gets
``claude.md`` and ``deepseek-flash`` gets ``deepseek.md``. A model that matches
nothing gets ``default.md``.

A model prompt replaces the default as a whole. To keep the default and add to
it, put ``{{base}}`` where the default text should go:

.. code-block:: text

   {{base}}

   Answer without a preamble.

To change a prompt without a rebuild, save a file in
``~/.config/llm-bridge/prompts/`` (``$XDG_CONFIG_HOME`` is honored). A file
there wins over the embedded one with the same name, and a new name adds a
prompt. The files are read on every request, so there is no need to restart.

The ``system`` field of a prompt and the ``-system-prompt`` flag win over all of
this, in that order.
