Protocol
========

The bridge speaks a simple **JSON-lines** protocol over stdio. Each line you
write to the bridge's **stdin** is a command, a request. Each line the bridge
writes to its **stdout** is an event, a response or a notification. There is one
JSON object per line, no enclosing array and no newline inside a value.

Stdout is reserved for the protocol, so send the logs to a file (``-logfile`` or
``-debug``) or drop them. Nothing else goes to stdout.

.. note::

   A command line can carry large payloads (e.g. ``set_knowledge_bases``
   metadata). The bridge's scanner accepts lines up to **8 MB** so a big line
   does not kill the process.

Commands (client → bridge)
--------------------------

Every command has the shape:

.. code-block:: json

   {"method": "prompt", "params": {}}

``prompt``
    Send a message to the LLM. When the text starts with ``#`` it runs a hook
    instead (see :doc:`hooks`).

    .. code-block:: json

       {
         "method": "prompt",
         "params": {
           "text": "add a test for parseHook",
           "model": "",
           "provider": "deepseek",
           "thinking": true,
           "reasoning_effort": "high",
           "system": "optional overriding system prompt",
           "system_prompt": "alias for system",
           "images": [{"path": "screenshot.png"}]
         }
       }

    All fields except ``text`` are optional **overrides** and, once set,
    persist across subsequent prompts:

    - ``model`` - force a model.
    - ``provider`` - switch the active LLM provider (``"deepseek"`` /
      ``"google"`` / ``"anthropic"``).
    - ``thinking`` - enable or disable thinking mode.
    - ``reasoning_effort`` - the reasoning effort, as one word on the common
      scale (``low``, ``medium``, ``high``, ``xhigh``, ``max``) or as a number of
      tokens for the reasoning budget. A level the provider does not know falls
      back to its default, and an empty string stops sending the parameter.
    - ``system`` / ``system_prompt`` - override the system prompt.

    ``images`` attaches one or more images to the prompt (see `Images`_ below).

.. _Images:

Images
~~~~~~

A prompt may carry images via the ``images`` array. Each entry is resolved in
this order of precedence:

- ``url`` - an external ``http(s)`` link, passed through to the provider, which
  downloads it. DeepSeek takes it natively. Gemini wants a ``fileData`` URI, and
  the bridge downloads and inlines the image when the API rejects that.
- ``data`` - inline base64, either raw or as a full data URL
  (``data:<mime>;base64,<payload>``). Use it for an image that only exists in
  your client, like a clipboard paste, with no file on disk to read.
- ``path`` - a local file, read by the bridge and embedded inline. A relative
  path resolves against the current cwd.

``mime_type`` optionally declares the media type. The supported formats are
PNG, JPEG, GIF and WebP, detected from the bytes and not from the name. An
image only attaches to a user message, and the active model has to support
vision, which on DeepSeek is the Flash model.

.. code-block:: json

   {
     "method": "prompt",
     "params": {
       "text": "What is in this screenshot?",
       "images": [{"path": "screenshot.png"}]
     }
   }

.. code-block:: json

   {
     "method": "prompt",
     "params": {
       "text": "Describe this.",
       "images": [{"url": "https://example.com/image.jpg"}]
     }
   }

``tool_confirm``
    Approve a mutating tool the bridge offered via a ``tool_confirm`` event. The
    bridge then runs the tool itself and appends its result. To deny a tool, send
    a ``cancel`` instead.

    .. code-block:: json

       {"method": "tool_confirm", "params": {"id": "call_123"}}

``tool_result`` *(legacy)*
    Formerly the client's reply carrying the result of a tool it had run. The
    bridge now runs the tools itself, so this command is **ignored** (a no-op),
    kept only for backwards compatibility with older clients.

    .. code-block:: json

       {"method": "tool_result", "params": {"id": "call_123", "result": "<string output>"}}

``cancel``
    Interrupt the in-flight streaming ``Chat`` immediately. The bridge replies
    with a ``cancelled`` event. Handled by the reader goroutine so it does not
    wait for the model to finish talking.

    .. code-block:: json

       {"method": "cancel"}

``set_cwd``
    Set the working directory. Also rebuilds the project knowledge base to
    follow the new directory.

    .. code-block:: json

       {"method": "set_cwd", "params": {"cwd": "/home/juca/mysrc/llm-bridge"}}

``set_knowledge_bases``
    Set the list of active knowledge bases.

    .. code-block:: json

       {"method": "set_knowledge_bases", "params": {"bases": [{"project": "llm-bridge"}]}}

``quit``
    Ask the bridge to shut down cleanly.

    .. code-block:: json

       {"method": "quit"}

Events (bridge → client)
------------------------

``ready``
    Emitted once at startup, before the loop starts, to signal the bridge is
    ready to receive commands. It also announces what the bridge can be asked
    for, so the client offers the real names instead of keeping its own copy.

    .. code-block:: json

       {"event": "ready", "default_provider": "deepseek", "providers": [
          {"name": "anthropic", "model": "claude-haiku-5-5",
           "models": [{"id": "claude-sonnet-5-5", "window": 1000000},
                      {"id": "claude-opus-4-5", "window": 200000}],
           "reasoning": {"levels": ["low", "medium", "high", "xhigh", "max"],
                         "budget": true}},
          {"name": "deepseek", "model": "deepseek-chat",
           "models": [{"id": "deepseek-flash", "window": 1000000}],
           "reasoning": {"levels": ["low", "medium", "high"], "budget": false}}]}

    ``default_provider`` is the provider a prompt without a ``provider`` uses.
    In each entry, ``model`` is the model in use now, ``models`` are the ids
    known for that provider, ``window`` is the same number ``turn_end`` reports
    in ``context_window``, and ``reasoning`` says which ``reasoning_effort``
    values the provider takes: the ``levels`` in ascending order, and
    ``budget`` for a numeric token budget.

    ``models`` is what to offer, not an allow list. A prompt may name any model,
    and a provider the bridge does not know still gets announced with ``name``
    and ``model`` alone. A bridge started without a provider registry sends the
    bare ``{"event": "ready"}``, which is all an older client reads.

``chunk``
    A streaming fragment of the model's content. The client concatenates the
    ``text`` fragments.

    .. code-block:: json

       {"event": "chunk", "text": "The bridge "}

``thinking``
    A streaming fragment of the model's chain-of-thought, emitted before the
    content chunks when thinking mode is on.

    .. code-block:: json

       {"event": "thinking", "text": "Let me check the tool loop..."}

``tool_call``
    A **read-only** tool (``read`` / ``grep`` / ``glob``) that the bridge already
    executed. Emitted for display only.

    .. code-block:: json

       {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}

``tool_confirm``
    A **mutating** call (``shell`` / ``write`` / ``search_replace`` / ``curl``,
    or a ``knowledge`` add/delete/reset) the bridge is offering for approval.
    The client replies with a ``tool_confirm`` command to allow it (the bridge
    then runs it) or with ``cancel`` to abort the turn.

    For a ``shell`` call the event carries three optional fields describing what
    the approval really does: ``segments`` (the commands the one-liner runs),
    ``risk`` (``high`` / ``medium`` / ``low``) and ``tainted`` (the arguments
    quote content fetched earlier in the session). A ``write`` or
    ``search_replace`` carries ``risk`` alone, ``high`` when the path is inside
    a ``.llm-bridge`` directory. A client that does not know them can ignore
    them. See :doc:`tools`.

    .. code-block:: json

       {"event": "tool_confirm", "id": "call_123", "name": "shell", "input": {"command": "go test ./..."}}

    .. code-block:: json

       {"event": "tool_confirm", "id": "call_124", "name": "shell",
        "input": {"command": "go build ./... && rm -rf /tmp/x | sh"},
        "segments": ["go build ./...", "rm -rf /tmp/x", "sh"],
        "risk": "high", "tainted": true}

``turn_end``
    End of a turn, with the stop reason, the model used and token counts.
    ``context_tokens`` is the size of the context actually sent on the **last**
    provider call of the turn (the full prompt: system prompt, history, tool
    results and images), and ``context_window`` is that model's static context
    window. ``context_pct`` is ``context_tokens / context_window``, a fraction
    between ``0`` and ``1``. When the model is not in the static per-model table
    (see :ref:`providers-context-window`) ``context_pct`` is ``null`` and
    ``context_window`` is ``0``, rather than guessing a window.
    ``cache_hit_tokens`` / ``cache_miss_tokens`` are the prompt cache accounting
    summed over every provider call of the turn, and a tool-calling turn makes
    several. A provider without cache reporting leaves them at ``0``.
    ``duration_ms`` is the wall time from the ``prompt`` to this event.
    ``llm_ms`` is the part spent in provider calls and ``tool_ms`` the part
    spent running tools. The time a ``tool_confirm`` waits for you is in
    ``duration_ms`` only, so ``duration_ms - llm_ms - tool_ms`` is the wait plus
    the bridge's own overhead. A cancelled turn sends no ``turn_end``.
    ``stop_reason`` is one of ``end_turn``, ``tool_use`` or ``max_tokens``
    whatever the provider, plus the provider's own word for a stop the common
    three do not name (a Gemini safety block, for example).

    .. code-block:: json

       {
         "event": "turn_end",
         "stop_reason": "end_turn",
         "context_pct": 0.42,
         "context_tokens": 420000,
         "context_window": 1000000,
         "model": "deepseek-flash",
         "input_tokens": 1024,
         "output_tokens": 512,
         "total_tokens": 1536,
         "cache_hit_tokens": 900,
         "cache_miss_tokens": 124,
         "duration_ms": 8420,
         "llm_ms": 7100,
         "tool_ms": 1180
       }

``usage_delta``
    Incremental token usage for the turn.

    .. code-block:: json

       {"event": "usage_delta", "input_tokens": 128, "output_tokens": 64, "total_tokens": 192}

``files_changed``
    The file paths written by ``write`` / ``search_replace`` in the turn that
    just ended, reads excluded. It arrives after ``turn_end``, so your client can
    refresh a buffer or run the tests.

    .. code-block:: json

       {"event": "files_changed", "files": ["/home/juca/mysrc/llm-bridge/server/server.go"]}

``hook_action``
    Result of a :doc:`hooks` run. Exactly one of ``output`` or ``error`` is set.

    .. code-block:: json

       {"event": "hook_action", "name": "algo", "output": "..."}

    A hook failure reports the reason instead:

    .. code-block:: json

       {"event": "hook_action", "name": "algo", "error": "hook not found"}

``error``
    An error response to the previous command.

    .. code-block:: json

       {"event": "error", "message": "cannot send new prompt while awaiting tool results"}

``cancelled``
    Reply to a ``cancel`` command, confirming the turn was interrupted.

    .. code-block:: json

       {"event": "cancelled"}
