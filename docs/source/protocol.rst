Protocol
========

The bridge speaks a simple **JSON-lines** protocol over stdio. Each line the
client writes to the bridge's **stdin** is a command (a request); each line the
bridge writes to its **stdout** is an event (a response or notification).
There is one JSON object per line, no enclosing array and no newline inside a
value.

Because stdout is reserved for the protocol, all logging goes to a file
(via ``-logfile`` or ``-debug``) or is disabled; nothing is ever written to
stdout except these JSON-lines events.

.. note::

   ``set_knowledge_bases`` and ``tool_result`` commands can carry large
   payloads (a whole file read, a ``grep`` with many matches or a ``shell``
   with lots of output). The bridge's scanner accepts lines up to **8 MB** to
   avoid killing the process on a big tool result.

Commands (client → bridge)
--------------------------

Every command has the shape:

.. code-block:: json

   {"method": "prompt", "params": {}}

``prompt``
    Send a message or instruction to the LLM (or, if the text starts with
    ``#``, run a :doc:`hooks` instead).

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
           "system_prompt": "alias for system"
         }
       }

    All fields except ``text`` are optional **overrides** and, once set,
    persist across subsequent prompts:

    - ``model`` — force a model.
    - ``provider`` — switch the active LLM provider (``"deepseek"`` /
      ``"google"``).
    - ``thinking`` — enable/disable thinking mode.
    - ``reasoning_effort`` — reasoning effort for DeepSeek, or a numeric
      thinking budget for Google.
    - ``system`` / ``system_prompt`` — override the system prompt.

``tool_result``
    Reply to a ``tool_call`` the model requested. Sent after the client runs
    the tool (read/write/shell/grep/glob/search_replace) locally.

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
    ready to receive commands.

    .. code-block:: json

       {"event": "ready"}

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
    The model requested a tool. The client runs the tool and replies with a
    ``tool_result``.

    .. code-block:: json

       {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}

``turn_end``
    End of a turn, with the stop reason, the model used and token counts.
    ``context_pct`` may be ``null``. ``cache_hit_tokens`` / ``cache_miss_tokens``
    are the prompt cache accounting summed over every provider call made in the
    turn (a tool-calling turn makes several); providers without cache reporting
    leave them at ``0``.

    .. code-block:: json

       {
         "event": "turn_end",
         "stop_reason": "stop",
         "context_pct": 0.42,
         "model": "deepseek-reasoner",
         "input_tokens": 1024,
         "output_tokens": 512,
         "total_tokens": 1536,
         "cache_hit_tokens": 900,
         "cache_miss_tokens": 124
       }

``usage_delta``
    Incremental token usage for the turn.

    .. code-block:: json

       {"event": "usage_delta", "input_tokens": 128, "output_tokens": 64, "total_tokens": 192}

``files_changed``
    Lists the file paths written or modified by the ``write`` / ``search_replace``
    tools during the just-completed turn (reads are not included). Emitted
    after ``turn_end``. The client can use it to refresh buffers or trigger
    automated tests.

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

       {"event": "error", "message": "tool_result without pending tool call"}

``cancelled``
    Reply to a ``cancel`` command, confirming the turn was interrupted.

    .. code-block:: json

       {"event": "cancelled"}
