Hooks
=====

A hook is a local script that runs instead of calling the LLM. Any ``prompt``
whose text starts with ``#`` is treated as a hook request: the first
whitespace-delimited token after ``#`` is the hook name and the rest is passed
as arguments to the script.

For example, ``#algo foo bar`` runs the script ``algo.sh`` with the arguments
``foo bar``. Hooks never touch the conversation history nor the LLM.

Script lookup
-------------

Scripts live in a ``hooks`` directory under ``.llm-bridge``, mirroring how
:doc:`context` files are found:

#. **Project directory** first: ``<cwd>/.llm-bridge/hooks/<name>.sh``.
#. Then the **general directory**: ``~/.llm-bridge/hooks/<name>.sh``.

The hook name is restricted to ``[A-Za-z0-9_-]+``. This prevents path
traversal — a name like ``../x``, ``a/b`` or ``a b`` is rejected with an error
instead of being turned into a filesystem path. An empty name is also
rejected.

Execution
---------

The script is executed with ``bash`` in the project's working directory, so it
sees the same directory as the conversation. If the working directory is unset
or absent, the process's own working directory is used.

The script's **combined stdout+stderr** is captured. On success it is returned
to the client as a single ``hook_action`` event with the ``output`` field set;
if the script cannot be located, has an invalid name or exits non-zero, the
``hook_action`` event carries the failure reason in its ``error`` field.

Behavior
--------

- **Non-blocking**: a slow hook (e.g. a build) does not block the bridge, so
  other prompts, tool results and cancels keep being processed while the hook
  executes.
- **Not cancellable**: a script, once launched, runs to completion. A
  ``cancel`` only interrupts a streaming answer, never a hook.

The wire format of the ``hook_action`` event is described in :doc:`protocol`.

Implementation details (script resolution and execution, and how hooks run
alongside the main loop) live in :doc:`/hacking/hooks-internals`.
