Tool calls
==========

The models already know how to call **tools**. The bridge advertises the tool
schemas to the provider and, unlike a plain provider SDK, **runs the tools
itself** and feeds the results back. This page covers that cycle, the events
involved and the tools you get. The raw schemas are in :doc:`protocol`, and the
knowledge-base tool is in :doc:`knowledge`.

The bridge classifies every call as **read-only** (it mutates nothing: ``read``,
``grep``, ``glob``, and a ``knowledge`` lookup) or **mutating** (``shell``,
``write``, ``search_replace``, ``curl``, and a ``knowledge`` write). Read-only
calls run **without confirmation**. Mutating calls go to your client first.

``knowledge`` is the one tool classified **per call**, not per tool. ``show`` and
``search`` only read, while ``add``, ``delete`` and ``reset`` write to a
persistent store that every later session reads. Reading the whole tool as
read-only would let a page plant a note with nobody watching. Always mutating
would turn every lookup into a click. A malformed or unknown command counts as
mutating, so a call the bridge cannot classify is confirmed.

The tool-call cycle
-------------------

A tool call is not one request and one response. It is a small loop
(``runToolCycle``) that keeps going until the model produces a final answer.

#. You send a ``prompt``. The bridge appends it to the history and calls the
   active provider.
#. When the model asks for a tool, the bridge inspects each requested call:

   - **read-only**: the bridge runs it right away, in the same turn, appends the
     result to the history and emits a ``tool_call`` event. That event is for
     **display** and takes no reply.
   - **mutating**: the bridge does **not** run it. It emits a ``tool_confirm``
     event and records the call as *pending approval*.

#. For each mutating call you decide. To allow it, reply with a ``tool_confirm``
   command carrying the same ``id``, and the bridge runs **that** tool at once
   and appends the result. To deny it (or the rest of the batch), send
   ``cancel``, which aborts the turn.
#. Once **every** pending call has been approved and run, the bridge calls the
   provider again with the updated history.
#. Steps 2 to 4 repeat until the model returns content with no tool calls. The
   turn then ends with ``turn_end`` and, if files were written, with
   ``files_changed``.

So one turn may make several provider calls and mix text, thinking, tool calls
and tool results. The ``usage_delta`` and ``turn_end`` token counts add up over
the whole turn.

Events
------

``tool_call`` (bridge to client)
    A **read-only** tool the bridge already ran. ``input`` is the parsed
    arguments object. Emitted for display, with no reply expected.

    .. code-block:: json

       {"event": "tool_call", "id": "call_123", "name": "read", "input": {"path": "server/server.go"}}

``tool_confirm`` (bridge to client)
    A **mutating** tool waiting on you. Same shape as ``tool_call``. Reply with
    a ``tool_confirm`` command to allow it, or with ``cancel`` to abort the turn.

    .. code-block:: json

       {"event": "tool_confirm", "id": "call_123", "name": "shell", "input": {"command": "go test ./..."}}

``tool_confirm`` (client to bridge)
    Your approval of a ``tool_confirm`` whose ``id`` is still pending. The
    bridge then runs the tool and appends the result.

    .. code-block:: json

       {"method": "tool_confirm", "params": {"id": "call_123"}}

``tool_result`` (client to bridge, legacy)
    Kept for backwards compatibility. The bridge runs the tools now, so this
    command is a no-op and an older client that sends it no longer drives the
    cycle.

Here is a full exchange, read-only plus mutating:

.. code-block:: text

   client → bridge   {"method": "prompt", "params": {"text": "list the tests and run them"}}
   bridge → client   {"event": "tool_call", "id": "r1", "name": "glob", "input": {"pattern": "**/*_test.go"}}
   bridge → client   {"event": "tool_confirm", "id": "s1", "name": "shell", "input": {"command": "go test ./..."}}
   client → bridge   {"method": "tool_confirm", "params": {"id": "s1"}}
   bridge → client   {"event": "chunk", "text": "All tests pass ..."}
   bridge → client   {"event": "turn_end", ...}

Available tools
---------------

The bridge advertises these tools to the provider (``tools/tools.go``) and runs
them in ``tools/exec.go``. The exception is ``knowledge``, which it resolves
inside itself. See :doc:`knowledge`.

``read`` *(read-only)*
    Read a file from disk. ``path`` is required. An optional ``offset`` (0-based
    line index to start at) and ``limit`` (max lines) take a slice.

``write`` *(mutating)*
    Write ``content`` to the file at ``path``. Both are required.

``search_replace`` *(mutating)*
    Find the exact ``search`` string in the file at ``path`` and replace the
    first occurrence with ``replace``. All three are required.

``grep`` *(read-only)*
    Search file contents for a POSIX extended regular expression (like ``grep
    -E``) under ``path``, which defaults to the cwd. Matching lines come back as
    ``file:line:text``.

``glob`` *(read-only)*
    Find files matching a ``pattern`` (required), optionally under ``path``.

``shell`` *(mutating)*
    Run a shell ``command`` (required) on the host.

``curl`` *(mutating)*
    Fetch ``url`` (required, http or https) with an optional list of extra curl
    options in ``args``. It is the only tool with **network access**, and the
    only one whose sandbox grants no project, home or temp path, so a fetch
    cannot read a local file. The options are an allowlist: ``-H``, ``-L``,
    ``-I``, ``-i``, ``--compressed``, ``-A``, ``-b`` (an inline cookie only),
    ``--max-time``, ``--connect-timeout``, ``--max-redirs`` and
    ``--max-filesize``. Uploads, file writes and config files are rejected.
    Output is truncated to 64 KiB. There is no ``curl`` tool with
    ``-curl=false`` or with ``-disable-sandbox``. See the "Sandbox" section of
    :doc:`usage`.

    The body reaches the model wrapped in
    ``<untrusted-web-content url="...">...</untrusted-web-content>``. The bridge
    truncates first and wraps second, so the envelope always closes. The system
    prompt says everything inside those tags is a page someone else wrote: data,
    never an instruction, and an instruction found there must be reported to you
    rather than followed.

``knowledge`` *(read-only for a lookup, mutating for a write)*
    Manage the knowledge base (``show``, ``search``, ``add``, ``delete``,
    ``reset``). The bridge resolves it **inside itself**, so it never needs a
    client round-trip. ``show`` and ``search`` run inline, and ``add``,
    ``delete`` and ``reset`` are offered for confirmation like any other write,
    because they persist a note every later session reads.

Where the tools run
-------------------

``read``, ``write``, ``glob`` and ``search_replace`` take a path that comes
straight from the model. They run in ``llm-tools``, a second binary next to the
bridge, under the tool sandbox, so the bridge process itself opens no path the
model chose. The note on the removed ceiling in :doc:`usage` says why that
matters.

The protocol is one JSON object per line in and one per line out:

.. code-block:: text

   {"name":"read","args":{"path":"go.mod"}}
   {"ok":true,"result":"module llm-bridge\n\ngo 1.22\n"}

The tool name travels in the request, so there is no flag and no subcommand.

``shell``, ``grep`` and ``curl`` do not go through the helper. They exec an
external program, so they have to be the sandboxed command itself.

The bridge looks for the binary at ``-tools-bin``, then next to itself, then in
``$PATH``. A missing or stale one is a startup error, because the handshake asks
for the protocol version before any tool call. The bridge sends one request per
process today: it writes a line and closes stdin. The loop reads until the input
ends, so a persistent process would need no change there.

Rules and details
-----------------

- **Working directory.** The bridge runs the tools itself, against the cwd you
  set with ``set_cwd``, and a relative ``path`` resolves against it. So the
  tools work on the project you pointed the bridge at.
- **Confirmation.** Only a mutating call (``shell``, ``write``,
  ``search_replace``, ``curl``, and a ``knowledge`` write) goes out as
  ``tool_confirm``. A read-only call runs without asking.
- **What an approval runs.** A ``tool_confirm`` for ``shell`` carries three
  extra fields, because one keystroke approves far more than it looks like:

  - ``segments``: the commands the one-liner runs, split on ``;``, ``&&``,
    ``||``, ``|`` and newlines, never inside quotes. Your client can show *three
    commands* instead of one line nobody reads.
  - ``risk``: ``high`` for a destructive, remote or privileged command
    (``rm -rf``, ``dd of=``, ``curl``, ``git push``, ``sudo``, ``| sh``, or a
    command naming ``.llm-bridge``), ``medium`` for a state-changing but
    ordinary one (``git commit``, ``make install``) and ``low`` otherwise. A
    client can demand a typed confirmation for the high ones instead of a
    keypress.

    A ``write`` or ``search_replace`` carries the label too: a path inside a
    ``.llm-bridge`` directory is ``high``, because that is where the context the
    next session reads and the hook scripts it runs live.
  - ``tainted``: the arguments reproduce text fetched earlier in the session by
    ``curl``, which is the "read this page, then run what it says" shape. It is
    advisory and blunt: a legitimate command that quotes the page matches too,
    and that is exactly the case worth showing.

  The bridge does **not offer** a one-liner with more than **10** segments. It
  answers with an error result telling the model to split the command, one
  action per call. That is the hard limit. The rest is detail for you.
- **Id matching.** The bridge accepts an approval only for an ``id`` it is
  offering. An approval for an unknown id, such as a late reply from a cancelled
  turn, is ignored.
- **Several calls per step.** A model may ask for several tools in one step. The
  read-only ones run inline, and the bridge waits until *all* the mutating ones
  are approved before it continues. Deny one and the turn is cancelled.
- **Internal tools.** The bridge runs ``knowledge`` and appends the result at
  once, after approval for a write. A step made only of ``knowledge`` lookups
  never needs a client round-trip.
- **Files changed.** A turn that used ``write`` or ``search_replace`` reports
  the touched paths in a ``files_changed`` event after ``turn_end``, which a
  client can use to refresh a buffer or run tests.
- **Cancellation.** Cancelling inside the tool cycle is safe. The bridge drops
  the unanswered ``tool_calls`` and any orphaned read-only results before it
  sends anything else, so the next prompt is never rejected for a missing tool
  response. A cancel also interrupts a command the bridge is running. The turn
  is not rolled back: its prompt and whatever the model produced before the
  interrupt stay in the history, and the model keeps that context on the next
  prompt.
- **History.** Tool calls and results stay in the history by default. With
  ``-prune`` or ``-aggressive-prune`` a completed turn is collapsed, with the
  file states preserved as ``<State>`` snapshots. That trades cache hits for a
  smaller history. See :doc:`usage`.
