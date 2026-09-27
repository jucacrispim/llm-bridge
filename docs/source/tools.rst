Tool calls
==========

The models already know how to call **tools**: the bridge simply advertises the
tool schemas to the provider and relays each call to the client, which runs the
tool and sends back the result. This document describes that cycle, the two
protocol events involved and the tools the bridge exposes. The raw event and
command schemas live in :doc:`protocol`; the knowledge-base tool is detailed in
:doc:`knowledge`.

The tool-call cycle
-------------------

A tool call is not a single request/response: it is a small loop
(``runToolCycle``) that keeps going until the model produces a final answer.

#. The client sends a ``prompt``. The bridge appends it to the conversation
   history and calls the active provider.
#. If the model decides to use a tool, the bridge emits a ``tool_call`` event
   (``id``, ``name``, ``input``) **instead of** finishing the turn, records the
   call as *pending* and waits.
#. The client runs the tool locally (it has direct access to the host and the
   project on disk) and replies with a ``tool_result`` carrying the same ``id``.
#. The bridge appends the result to the history. Once **every** pending call has
   a result, it calls the provider again with the updated history.
#. Steps 2–4 repeat until the model returns content with no tool calls; the turn
   then ends with ``turn_end`` (and, if files were written, ``files_changed``).

A single turn may therefore make several provider calls and mix text, thinking,
tool calls and tool results. The ``usage_delta`` and ``turn_end`` token counts
are accumulated over the whole turn.

Events
------

``tool_call`` (bridge → client)
    The model requested a tool. ``input`` is the parsed arguments object.

    .. code-block:: json

       {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}

``tool_result`` (client → bridge)
    The client's reply, after running the tool. ``result`` is the tool output as
    a string; ``id`` must match a ``tool_call`` that is still pending.

    .. code-block:: json

       {"method": "tool_result", "params": {"id": "call_123", "result": "<string output>"}}

A full exchange looks like:

.. code-block:: text

   client → bridge   {"method": "prompt", "params": {"text": "read server/server.go"}}
   bridge → client   {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}
   client → bridge   {"method": "tool_result", "params": {"id": "call_123", "result": "<file contents>"}}
   bridge → client   {"event": "chunk", "text": "The file ..."}
   bridge → client   {"event": "turn_end", ...}

Available tools
---------------

The bridge advertises these tools to the provider (``tools/tools.go``). The
**client** implements them, except ``knowledge``, which the bridge resolves
internally (see :doc:`knowledge`).

``read``
    Read a file from disk. ``path`` (required); optional ``offset`` (0-based
    line index to start at) and ``limit`` (max number of lines) select a slice.

``write``
    Write ``content`` to the file at ``path`` (both required).

``search_replace``
    Search for an exact ``search`` string in the file at ``path`` and replace
    the first occurrence with ``replace`` (all required).

``grep``
    Search file contents for a POSIX extended regular expression
    (like ``grep -E``) in ``path`` (optional, defaults to the cwd). Returns
    matching lines as ``file:line:text``.

``glob``
    Find files matching a ``pattern`` (required), optionally under ``path``.

``shell``
    Execute a shell ``command`` (required) on the host.

``knowledge``
    Manage the project's local knowledge base (``show`` / ``search`` / ``add`` /
    ``delete`` / ``reset``). Resolved **inside the bridge**, so it never needs a
    client round-trip.

Rules and details
-----------------

- **Line size:** a ``tool_result`` is a single JSON-line written to the bridge's
  stdin; the scanner accepts lines up to **8 MB** so a whole file read or a
  large ``shell`` output fits.
- **Id matching:** the bridge only accepts a ``tool_result`` for an ``id`` it is
  currently waiting on. A result for an unknown id (e.g. a late reply from a
  cancelled turn) is silently ignored; a duplicate result for the same id
  returns an ``error``.
- **Multiple calls per step:** a model may request several tools in one step.
  The bridge waits until *all* of their ids have results before continuing.
- **Internal tools:** ``knowledge`` is executed by the bridge and its result
  appended immediately, so a step made only of ``knowledge`` calls does not wait
  on the client.
- **Files changed:** a turn that used ``write`` or ``search_replace`` reports the
  touched paths in a ``files_changed`` event after ``turn_end`` (used, for
  example, to refresh buffers or trigger tests).
- **Cancellation:** cancelling a turn in the middle of the tool cycle is safe —
  the bridge drops the unanswered ``tool_calls`` before sending anything else, so
  the next prompt is never rejected for a missing tool response.
- **History:** tool calls and results are kept in the history by default. With
  ``-prune`` / ``-aggressive-prune`` a completed turn is collapsed (file states
  preserved as ``<State>`` snapshots), trading cache hits for a smaller history;
  see :doc:`usage`.
