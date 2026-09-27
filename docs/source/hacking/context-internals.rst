Context loading internals
=========================

Implemented in ``context/context.go``. The package exposes:

``Load(cwd)``
    Reads the general directory (``~/.llm-bridge``) first and then the local
    one (``<cwd>/.llm-bridge``), concatenating the entries in that order. If
    ``cwd`` is empty it falls back to the process's current working directory.

``readDir(dir)``
    Globs ``*.md``, sorts them alphabetically and reads each file's content
    (trimmed of surrounding whitespace). A missing directory is ignored without
    error; a file that fails to read is skipped with a log.

``BuildMessages(entries)``
    Converts the loaded entries into **persistent** (non-ephemeral) user
    messages. Each file becomes one user message whose content is the rendered
    entry.

Each entry is wrapped in the context markers:

.. code-block:: text

   --- CONTEXT ENTRY BEGIN ---
   [<path>]
   <content>
   --- CONTEXT ENTRY END ---

The ``[<path>]`` line carries the file's original path, so the model can tell
where each block came from.

The project/general lookup mirrors how :doc:`hooks-internals` scripts are
resolved (project first, then the user's home).
