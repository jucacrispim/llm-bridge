Context
=======

The bridge loads context files that are injected into the conversation. These
are Markdown files read from two sources:

- **General context**: ``~/.llm-bridge/*.md`` — applies to all conversations.
- **Local context**: ``<cwd>/.llm-bridge/*.md`` — applies to the current
  project/directory.

The general context comes **before** the local one. Within each directory the
files are sorted **alphabetically**. Each file becomes a persistent
(non-ephemeral) user message that keeps its original path.

Rendering
---------

Each file is wrapped in the context markers and injected as a single user
message:

.. code-block:: text

   --- CONTEXT ENTRY BEGIN ---
   [<path>]
   <content>
   --- CONTEXT ENTRY END ---

The ``[<path>]`` line carries the file's original path, so the model can tell
where each block came from.

Load logic
----------

Implemented in ``context/context.go``:

- ``Load(cwd)`` reads the general directory (``~/.llm-bridge``) first and then
  the local one (``<cwd>/.llm-bridge``), concatenating the entries in that
  order.
- ``readDir(dir)`` globs ``*.md``, sorts them alphabetically and reads each
  file's content (trimmed of surrounding whitespace). A missing directory is
  ignored without error; a file that fails to read is skipped with a log.
- If ``cwd`` is empty, ``Load`` falls back to the process's current working
  directory.

How it is used
--------------

On the **first turn** (see :doc:`architecture`), the server loads the context
and appends each entry as a user message at the top of the history, right
before the user's first prompt. On that same first turn it also appends a
one-line note about the project knowledge base when the KB is enabled. The
context is only loaded once — afterwards it is just part of the conversation
history.

The project/general lookup mirrors how :doc:`hooks` scripts are resolved
(project first, then the user's home).
