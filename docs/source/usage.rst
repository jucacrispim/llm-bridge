Usage
=====

How to build, run and configure the bridge.

Build
-----

The default build produces the binary under ``build/``:

.. code-block:: sh

   $ make build          # → build/llm-bridge

The knowledge base build (with the ONNX embedder) requires the
``knowledge_onnx`` build tag, CGO and the libtokenizers library:

.. code-block:: sh

   $ make build-kb       # CGO_ENABLED=1 + -tags knowledge_onnx + CGO_LDFLAGS

For the KB to work it also needs the model/tokenizer assets (see
:doc:`knowledge`):

.. code-block:: sh

   $ scripts/fetch_kb.sh
   $ export LLM_BRIDGE_ONNXRUNTIME_LIB="$HOME/.cache/llm-bridge/libonnxruntime.so"

Without these the KB starts disabled with a warning — the bridge does not
break. Run the test suite with ``make test`` (or ``go test ./...``).

Run
---

The bridge is a stdio process: it reads JSON-lines commands from stdin and
writes JSON-lines events to stdout. The client typically spawns it as a
subprocess.

.. code-block:: sh

   $ ./build/llm-bridge [flags]

Because stdout is reserved for the protocol, logs must be sent to a file or
disabled, or they will corrupt the client stream:

.. code-block:: sh

   $ ./build/llm-bridge -logfile /tmp/llm-bridge.log

The protocol (commands and events) is described in :doc:`protocol`.

CLI flags
---------

``--provider <name>``
    The default/initial LLM provider: ``deepseek`` (default) or ``google``.
    Both providers are registered up front, so a prompt can still switch the
    active provider per request via the ``provider`` override.

``--model <name>``
    Force a model. When it is the default provider it overrides
    ``DEEPSEEK_MODEL`` / ``GOOGLE_MODEL``. For DeepSeek without any explicit
    model, the model is derived from the thinking mode
    (``deepseek-reasoner`` / ``deepseek-chat``).

``--thinking[=true|false]``
    Enable/disable thinking mode (default ``true``). When no explicit model is
    set for DeepSeek, this also picks the default model. Honored per request by
    the ``thinking`` override.

``--reasoning-effort <value>``
    When thinking is on: a reasoning effort for DeepSeek (``low``/``medium``/
    ``high``, default ``high``) or a numeric thinking budget for Google.

``--system-prompt <path-or-text>``
    Override the system prompt. If the value is a path to an existing file its
    contents are used, otherwise it is treated as a literal system prompt
    string.

``-prune``
    Collapse reasoning and tool results of file tools into user snapshots
    (``<State>`` / ``<Replace>``), preserving file states while dropping the
    intermediate tool calls and chain-of-thought from the history. Saves tokens
    and keeps the prefix cacheable.

``--aggressive-prune``
    Collapse each completed tool-calling turn into just the user prompt plus
    the final answer, dropping the intermediate tool calls, tool results and
    chain-of-thought entirely. Mutually exclusive with ``-prune``.

``--knowledge[=true|false]``
    Enable/disable the project knowledge base and its ``knowledge`` tool
    (default ``true``).

``--knowledge-base <dir>``
    Base directory for the project knowledge bases (default
    ``~/.local/share/llm-bridge/knowledge_bases``; ``KNOWLEDGE_BASE`` env also
    honored).

``--kb-load <lazy|eager>``
    When to load the (heavy) ONNX KB model: ``lazy`` (default — loads on first
    use so the server starts fast; a failure surfaces on that first
    ``knowledge`` operation) or ``eager`` (loads at startup; a failure disables
    the KB with a warning).

``-logfile <path>``
    Redirect all logs to a file (and enable trace level). Keeps the JSON-lines
    stdout clean.

``-debug``
    Compatibility flag: same as ``-logfile`` with the default
    ``/tmp/llm-bridge.log``.

``-populate-project-kb <seedDir> -project <project>``
    Standalone mode (does not run the server): fully rebuild a project's
    knowledge base from a seed directory and exit. Requires the ONNX embedder
    (``make build-kb``).

Environment variables
---------------------

DeepSeek:
    ``DEEPSEEK_API_KEY`` (required), ``DEEPSEEK_URL``,
    ``DEEPSEEK_MODEL``, ``DEEPSEEK_THINKING``, ``DEEPSEEK_REASONING_EFFORT``.

Google:
    ``GOOGLE_API_KEY`` / ``GEMINI_API_KEY`` (required), ``GOOGLE_URL``,
    ``GOOGLE_MODEL`` / ``GEMINI_MODEL``, ``GOOGLE_THINKING``,
    ``GOOGLE_THINKING_BUDGET``.

Knowledge base:
    ``KNOWLEDGE_BASE``, ``LLM_BRIDGE_KB_MODEL``, ``LLM_BRIDGE_KB_TOKENIZER``,
    ``LLM_BRIDGE_ONNXRUNTIME_LIB``.

See :doc:`providers` for the meaning of each provider variable and
:doc:`knowledge` for the KB ones.

Logging
-------

By default (no ``-logfile``/``-debug``) all logs are discarded, so stdout stays
reserved for the protocol. With ``-logfile <path>`` (or ``-debug``) the
logger switches to trace level and writes to that file.
