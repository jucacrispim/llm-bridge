Changelog
=========

All notable changes to llm-bridge are documented here. The format loosely
follows `Keep a Changelog <https://keepachangelog.com/>`_.

v0.3
----

Added
~~~~~

- Real per-model context usage reporting in ``turn_end``. The event now carries
  ``context_tokens`` (prompt tokens of the last call in the turn) and
  ``context_window`` (the model's static context window), and ``context_pct`` is
  computed as a fraction of that window instead of being hardcoded to ``0.0``.
  Unknown models report ``context_pct`` as ``null``.

Changed
~~~~~~~

- The default Google model is now ``gemini-3-flash``.

v0.2.0
------

Improved
~~~~~~~~

- The model is now encouraged to consult and feed the per-project knowledge
  base before reasoning from scratch about how the project works, its
  architecture, or past decisions:

  - new "Project knowledge base" section in the embedded system prompt;
  - the ``knowledge`` tool description now states *when* to use it;
  - the first-turn notice is in English and carries the search nudge.

v0.1.0
------

Initial release: unified protocol over JSON-lines/stdio, streaming, tool
calling, the DeepSeek and Google providers, thinking mode, images, hooks,
context files, the project knowledge base and history pruning.
