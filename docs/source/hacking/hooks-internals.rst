Hooks internals
===============

Implemented in ``hooks/hooks.go``. The package exposes:

``Lookup``
    Resolves a hook name to a script path, project directory first
    (``<cwd>/.llm-bridge/hooks/<name>.sh``), then the general directory
    (``~/.llm-bridge/hooks/<name>.sh``).

``validName``
    Restricts the hook name to ``[A-Za-z0-9_-]+``, preventing path traversal
    (a name like ``../x``, ``a/b`` or ``a b`` becomes an error instead of a
    filesystem path).

``Run``
    Executes the script with ``bash`` and ``cmd.Dir = cwd``, so it sees the
    same directory as the conversation (falling back to the process's own
    working directory when ``cwd`` is unset).

Hooks run inside ``handleLine`` (see :doc:`architecture`):

- The script runs in its own goroutine, so a slow hook does not block the main
  server loop; other commands keep being processed while it executes.
- The ``hook_action`` event is written through ``st.write``, which serializes
  on ``writeMut``, so it cannot interleave with the streaming events written
  by the main loop.
- A ``hook_action`` does **not** emit a ``turn_end``, so the client-side turn
  state must be finalized by the hook handler itself.
- Hooks are not cancellable: a ``cancel`` interrupts an in-flight ``Chat``,
  never a hook.

The wire format of the ``hook_action`` event is described in :doc:`/protocol`.
