Context
=======

The bridge can load context files that are injected into the conversation, so
the model starts every session already knowing the project conventions you
care about. These are plain Markdown files read from two sources:

- **General context**: ``~/.llm-bridge/*.md`` — applies to all conversations.
- **Local context**: ``<cwd>/.llm-bridge/*.md`` — applies to the current
  project/directory.

The general context comes **before** the local one. Within each directory the
files are sorted **alphabetically**. Each file is injected as a persistent
message at the very top of the conversation, keeping its original path so the
model can tell where each block came from.

When it is loaded
-----------------

The context is loaded on the **first turn** of the conversation (see
:doc:`/hacking/architecture`), right before your first prompt, and only once —
afterwards it is just part of the conversation history. No extra event is
emitted for it; the usual ``ready`` already signals the bridge is up.

The project/general lookup mirrors how :doc:`hooks` scripts are resolved
(project directory first, then your home).

.. note::

   This is separate from the :doc:`knowledge` base. Context files are
   pre-configured steering you write by hand; the knowledge base is content the
   model stores and retrieves during the conversation.

Implementation details (the ``context`` package, load order and rendering) live
in :doc:`/hacking/context-internals`.
