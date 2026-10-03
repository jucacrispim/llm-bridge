Tool calls
==========

The models already know how to call **tools**: the bridge advertises the tool
schemas to the provider and, unlike a plain provider SDK, **runs the tools
itself** and feeds the results back to the model. This document describes that
cycle, the protocol events involved and the tools the bridge exposes. The raw
event and command schemas live in :doc:`protocol`; the knowledge-base tool is
detailed in :doc:`knowledge`.

The bridge classifies every tool as **read-only** (it never mutates any state:
``read``, ``grep``, ``glob``, ``knowledge``) or **mutating** (``shell``,
``write``, ``search_replace``). Read-only tools run **without confirmation**;
mutating tools are first offered to the client for approval.

The tool-call cycle
-------------------

A tool call is not a single request/response: it is a small loop
(``runToolCycle``) that keeps going until the model produces a final answer.

#. The client sends a ``prompt``. The bridge appends it to the conversation
   history and calls the active provider.
#. If the model decides to use a tool, the bridge inspects each requested call:

   - **read-only** — the bridge runs the tool immediately (in the same turn),
     appends the result to the history and emits a ``tool_call`` event purely
     for **display** (the client does not reply to it);
   - **mutating** — the bridge does **not** run it yet: it emits a
     ``tool_confirm`` event and records the call as *pending approval*.

#. For every mutating call the client decides whether to allow it and, when it
   does, replies with a ``tool_confirm`` command carrying the same ``id``. Each
   approval is handled on its own: the bridge runs **that** tool immediately and
   appends its result. To deny a tool (or the rest of the batch) the client
   sends ``cancel`` instead, which aborts the turn.
#. Once **every** pending (mutating) call has been approved and run, the bridge
   calls the provider again with the updated history.
#. Steps 2–4 repeat until the model returns content with no tool calls; the turn
   then ends with ``turn_end`` (and, if files were written, ``files_changed``).

A single turn may therefore make several provider calls and mix text, thinking,
tool calls and tool results. The ``usage_delta`` and ``turn_end`` token counts
are accumulated over the whole turn.

Events
------

``tool_call`` (bridge → client)
    A **read-only** tool that the bridge already executed. ``input`` is the
    parsed arguments object. Emitted for display only — no reply is expected.

    .. code-block:: json

       {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}

``tool_confirm`` (bridge → client)
    A **mutating** tool the bridge is offering for approval. Same shape as
    ``tool_call``. The client replies with a ``tool_confirm`` command to allow it
    or with ``cancel`` to abort the turn.

    .. code-block:: json

       {"event": "tool_confirm", "id": "call_123", "name": "shell", "input": {"command": "go test ./..."}}

``tool_confirm`` (client → bridge)
    The client's approval of a ``tool_confirm`` whose ``id`` is still pending.
    The bridge then runs the tool itself and appends the result.

    .. code-block:: json

       {"method": "tool_confirm", "params": {"id": "call_123"}}

``tool_result`` (client → bridge, legacy)
    Kept only for backwards compatibility. Now that the bridge runs the tools,
    this command is **ignored** (a no-op); an older client that sends it no
    longer drives the tool cycle.

A full exchange (read-only + mutating) looks like:

.. code-block:: text

   client → bridge   {"method": "prompt", "params": {"text": "list the tests and run them"}}
   bridge → client   {"event": "tool_call", "id": "r1", "name": "glob", "input": {"pattern": "**/*_test.go"}}
   bridge → client   {"event": "tool_confirm", "id": "s1", "name": "shell", "input": {"command": "go test ./..."}}
   client → bridge   {"method": "tool_confirm", "params": {"id": "s1"}}
   bridge → client   {"event": "chunk", "text": "All tests pass ..."}
   bridge → client   {"event": "turn_end", ...}

Available tools
---------------

The bridge advertises these tools to the provider (``tools/tools.go``) and
executes all of them (``tools/exec.go``), except ``knowledge`` which it resolves
internally (see :doc:`knowledge`).

``read`` *(read-only)*
    Read a file from disk. ``path`` (required); optional ``offset`` (0-based
    line index to start at) and ``limit`` (max number of lines) select a slice.

``write`` *(mutating)*
    Write ``content`` to the file at ``path`` (both required).

``search_replace`` *(mutating)*
    Search for an exact ``search`` string in the file at ``path`` and replace
    the first occurrence with ``replace`` (all required).

``grep`` *(read-only)*
    Search file contents for a POSIX extended regular expression
    (like ``grep -E``) in ``path`` (optional, defaults to the cwd). Returns
    matching lines as ``file:line:text``.

``glob`` *(read-only)*
    Find files matching a ``pattern`` (required), optionally under ``path``.

``shell`` *(mutating)*
    Execute a shell ``command`` (required) on the host.

``knowledge`` *(read-only)*
    Manage the project's local knowledge base (``show`` / ``search`` / ``add`` /
    ``delete`` / ``reset``). Resolved **inside the bridge**, so it never needs a
    client round-trip.

Rules and details
-----------------

- **Working directory:** the bridge runs the tools itself, resolved against the
  cwd it was told via ``set_cwd`` (relative ``path`` arguments are resolved
  against it). The model's tools therefore operate on the project the client
  pointed the bridge at.
- **Confirmation:** only mutating tools (``shell`` / ``write`` /
  ``search_replace``) are offered for approval via ``tool_confirm``;
  read-only tools run without asking.
- **Id matching:** the bridge only accepts a ``tool_confirm`` approval for an
  ``id`` it is currently offering. An approval for an unknown id (e.g. a late
  reply from a cancelled turn) is silently ignored.
- **Multiple calls per step:** a model may request several tools in one step.
  The read-only ones run inline; the bridge waits until *all* mutating ones have
  been approved before continuing. If one is denied the turn is cancelled.
- **Internal tools:** ``knowledge`` is executed by the bridge and its result
  appended immediately, so a step made only of ``knowledge`` calls never needs a
  client round-trip.
- **Files changed:** a turn that used ``write`` or ``search_replace`` reports the
  touched paths in a ``files_changed`` event after ``turn_end`` (used, for
  example, to refresh buffers or trigger tests).
- **Cancellation:** cancelling a turn in the middle of the tool cycle is safe —
  the bridge drops the unanswered ``tool_calls`` (and any orphaned read-only
  results) before sending anything else, so the next prompt is never rejected
  for a missing tool response. Cancelling also interrupts a command the bridge
  may be running.
- **History:** tool calls and results are kept in the history by default. With
  ``-prune`` / ``-aggressive-prune`` a completed turn is collapsed (file states
  preserved as ``<State>`` snapshots), trading cache hits for a smaller history;
  see :doc:`usage`.
