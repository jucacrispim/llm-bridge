Context
=======

The bridge can load context files into the conversation, so the model starts
every session knowing the project conventions you care about. These are plain
Markdown files, read from two places:

- **General context**: ``~/.llm-bridge/*.md``, which applies to all
  conversations.
- **Local context**: ``<cwd>/.llm-bridge/*.md``, which applies to the current
  project or directory.

The general context comes **before** the local one, and inside a directory the
files are sorted **alphabetically**. Each file becomes a persistent message at
the very top of the conversation, with its original path in the block, so the
model can tell where each one came from.

When it is loaded
-----------------

The bridge loads the context on the **first turn**, right before your first
prompt, and only once. After that it is just part of the history. No event
announces it: the usual ``ready`` already says the bridge is up.

Both directories mirror how :doc:`hooks` scripts are resolved, project directory
first and then your home.

.. note::

   This is not the :doc:`knowledge` base. Context files are steering you write
   by hand. The knowledge base is content the model stores and retrieves during
   the conversation.

The ``context`` package, the load order and the rendering are in
:doc:`/hacking/context-internals`.
