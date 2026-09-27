Usage
=====

.. note::

   Currently only deepseek and google gemini are implemented as providers
   in the bridge. In future releases other providers may be implemented



This page covers how to run the bridge and every way of calling and using it:
as a stdio subprocess, through its CLI flags, through prompts and hooks, and in
the standalone knowledge-base mode.

Running the bridge
------------------

The bridge is a **stdio** process: it reads JSON-lines commands from stdin and
writes JSON-lines events to stdout. It does not open a network port; a client
(e.g. Emacs) spawns it as a subprocess and talks to it over its pipes. Because
of that, the bridge runs on the host and the model's tools operate directly on
the project directory.

.. code-block:: sh

   $ ./build/llm-bridge [flags]

Once it is up it emits a single ``ready`` event, and only then should the client
start sending commands.

Because stdout is reserved for the protocol, logs must go to a file or be
disabled, or they will corrupt the client stream:

.. code-block:: sh

   $ ./build/llm-bridge -logfile /tmp/llm-bridge.log

The full wire format (commands and events) is described in :doc:`protocol`.

Ways of using the bridge
------------------------

Once running, there are four ways to interact with the bridge:

**1. Prompts (the LLM path).** Send a ``prompt`` command and the bridge forwards
it to the active provider, streaming the answer back as ``chunk`` /
``thinking`` events (and ``tool_call`` when the model wants a tool). A prompt
can also carry **overrides** — ``provider``, ``model``, ``thinking``,
``reasoning_effort`` and ``system`` / ``system_prompt`` — which, once set,
persist across subsequent prompts. This is how a single session can switch
provider or change the model on the fly.

.. code-block:: json

   {"method": "prompt", "params": {"text": "add a test for parseHook"}}

A prompt can also carry **images** via the ``images`` array, given by local
``path``, external ``url`` or inline base64 ``data`` (the last is what a client
uses for a clipboard paste). See :ref:`Images <Images>` in :doc:`protocol` for
the field-by-field details and provider support.

.. code-block:: json

   {"method": "prompt", "params": {"text": "what is in this?", "images": [{"path": "screenshot.png"}]}}

**2. Hooks.** A prompt whose text starts with ``#`` is **not** sent to the LLM;
it runs a local script instead. See :doc:`hooks`.

.. code-block:: json

   {"method": "prompt", "params": {"text": "#algo foo bar"}}

**3. Tool results and control commands.** When the model requests an external
tool (``read``, ``write``, ``shell``, ``grep``, ``glob``, ``search_replace``)
the client runs it locally and replies with a ``tool_result``. The client can
also ``cancel`` an in-flight turn, ``set_cwd`` to move the working directory
(which also rebuilds the project knowledge base), ``set_knowledge_bases`` to
choose the active bases, and ``quit`` to shut down.

**4. Standalone knowledge-base population.** A mode that does not run the
server at all — it fully rebuilds a project's knowledge base from a seed
directory and exits (see :doc:`knowledge`):

.. code-block:: sh

   $ ./build/llm-bridge -populate-project-kb ~/.local/share/llm-bridge/seeds -project <project>

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
    (``deepseek-flash`` / ``deepseek-chat``).

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

The bridge reads these from the environment at runtime.

**DeepSeek**

* ``DEEPSEEK_API_KEY`` — API key (required).
* ``DEEPSEEK_URL`` — API endpoint (default ``https://api.deepseek.com/chat/completions``).
* ``DEEPSEEK_MODEL`` — explicit model; when unset, derived from thinking (``deepseek-flash`` / ``deepseek-chat``).
* ``DEEPSEEK_THINKING`` — enable thinking mode (default ``true``).
* ``DEEPSEEK_REASONING_EFFORT`` — reasoning effort sent when thinking is on (default ``high``).

**Google / Gemini**

* ``GOOGLE_API_KEY`` — API key (required).
* ``GEMINI_API_KEY`` — alternative API key, used when ``GOOGLE_API_KEY`` is unset.
* ``GOOGLE_URL`` — API endpoint base (default ``https://generativelanguage.googleapis.com/v1beta``).
* ``GOOGLE_MODEL`` — explicit model; when unset, falls back to ``GEMINI_MODEL`` then ``gemini-2.5-flash``.
* ``GEMINI_MODEL`` — alternative explicit model, used when ``GOOGLE_MODEL`` is unset.
* ``GOOGLE_THINKING`` — enable thinking mode (default ``true``).
* ``GOOGLE_THINKING_BUDGET`` — thinking token budget when thinking is on (default ``1024``).

**Knowledge base**

* ``KNOWLEDGE_BASE`` — base directory for the project KBs (default ``~/.local/share/llm-bridge/knowledge_bases``).
* ``LLM_BRIDGE_KB_MODEL`` — path to the ONNX model (default ``~/.cache/llm-bridge/model.onnx``).
* ``LLM_BRIDGE_KB_TOKENIZER`` — path to the tokenizer (default ``~/.cache/llm-bridge/tokenizer.json``).
* ``LLM_BRIDGE_ONNXRUNTIME_LIB`` — path to ``libonnxruntime.so``, required by the KB build.
* ``LLM_BRIDGE_KB_DIR`` — cache dir used by ``scripts/fetch_kb.sh`` to download the KB assets (default ``~/.cache/llm-bridge``; script only, not read by the bridge).

See :doc:`/hacking/providers` for the meaning of each provider variable and
:doc:`knowledge` for the KB ones.

Logging
-------

By default (no ``-logfile``/``-debug``) all logs are discarded, so stdout stays
reserved for the protocol. With ``-logfile <path>`` (or ``-debug``) the
logger switches to trace level and writes to that file.
