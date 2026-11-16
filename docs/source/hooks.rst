Hooks
=====

A hook is a local script that runs instead of the LLM. Any ``prompt`` whose text
starts with ``#`` is a hook request: the first whitespace-delimited token after
``#`` is the hook name, and the rest goes to the script as arguments.

So ``#algo foo bar`` runs ``algo.sh`` with the arguments ``foo bar``. A hook
never touches the conversation history or the LLM.

Script lookup
-------------

Scripts live in a ``hooks`` directory under ``.llm-bridge``, the same way
:doc:`context` files are found:

#. The **project directory** first: ``<cwd>/.llm-bridge/hooks/<name>.sh``.
#. Then the **general directory**: ``~/.llm-bridge/hooks/<name>.sh``.

A hook name is restricted to ``[A-Za-z0-9_-]+``. That keeps a name like ``../x``,
``a/b`` or ``a b`` from becoming a filesystem path: it is rejected with an
error. An empty name is rejected too.

Execution
---------

The bridge runs the script with ``bash`` in the project's working directory, so
it sees the same directory as the conversation. When the working directory is
unset or missing, the process's own working directory is used.

You get the script's **combined stdout+stderr**. On success it arrives as a
single ``hook_action`` event with ``output`` set. A script that cannot be found,
has an invalid name or exits non-zero arrives with the reason in ``error``.

Behavior
--------

- **Non-blocking.** A slow hook (a build, say) does not hold the bridge, so you
  can keep sending prompts, tool results and cancels while it runs.
- **Not cancellable.** A script runs to completion once launched. ``cancel``
  interrupts a streaming answer, never a hook.

The wire format of ``hook_action`` is in :doc:`protocol`, and the resolution and
execution code in :doc:`/hacking/hooks-internals`.
