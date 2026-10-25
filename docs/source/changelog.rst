Changelog
=========

Unreleased
----------

Added
~~~~~

- ``-kb-scope`` (``global``/``project``/``both``) selects which knowledge
  base(s) are in use, and a prompt can change it for the rest of the
  conversation with ``kb_scope`` (same persistence as ``model``/``provider``).
- ``knowledge/set.go``: a ``Set`` merges one or two managers behind a single
  ``Execute``. ``show`` and ``search`` cover every active KB (labels are
  prefixed with the scope when more than one is active); ``add``, ``delete``
  and ``reset`` take an optional ``scope`` and default to the global KB.
- ``-populate-project-kb ... -project _global`` (re)builds the shared KB from
  the global seeds only (there is no ``_global`` seed subdirectory).

Changed
~~~~~~~

- **The knowledge base is now shared across projects by default.** The KB used
  to follow ``set_cwd`` and store notes under ``knowledge_bases/<project>/``,
  so a note learned in one project was invisible everywhere else. The default
  is now a single KB under ``knowledge_bases/_global/``; ``-kb-scope project``
  restores the previous behavior and ``both`` uses the shared KB plus the
  project's.
- Existing project KBs are left untouched: the scope only changes which ones
  are read, nothing is migrated or discarded.
- ``set_cwd`` reloads the KB in the ``project``/``both`` scopes only; in the
  ``global`` scope it no longer rebuilds anything.
- The ``knowledge`` tool schema documents the ``scope`` argument; the tool
  stays read-only (``add``/``delete``/``reset`` are gated by the scope in the
  set, not by ``tool_confirm``).
- Docs and the system prompt describe the shared KB as the default.

v0.5.0
------

Added
~~~~~

- **Sandbox via** `caged <https://docs.poraodojuca.dev/caged>`_: on startup the
  bridge re-executes itself under ``caged`` (the *ceiling* layer) and every
  command it runs — the ``shell`` and ``grep`` tools and the hooks — runs under
  a second, stricter layer (*tool*). Landlock policies compose by intersection,
  so ``bridge = ceiling ∩ tool``. What the tool layer restricts, beyond the
  filesystem policy of the bridge itself, is the **environment** (no API keys
  reach a command), **IPC** (unix sockets, SysV) and the **network** (denied
  unless ``-sandbox-network`` is given).
- Flags: ``-caged-bin`` (env ``CAGED_BIN``, else ``caged`` from ``PATH``),
  ``-sandbox-ro``/``-sandbox-rw`` (repeatable; apply to the bridge and the
  tools), ``-sandbox-network``, ``-sandbox-connect-port`` (repeatable),
  ``-sandbox-tmp`` (private temporary directory, default
  ``/tmp/llm-bridge``, exported as ``TMPDIR``/``GOTMPDIR``/``TMP``/``TEMP``)
  and ``-disable-sandbox``.
- New ``sandbox`` package (``Policy``/``Args``/``Validate``,
  ``Sandbox.Command``/``Run``) with no cgo and no new dependencies.
- New section "Sandbox (caged)" in the usage docs.

Changed
~~~~~~~

- **The sandbox is on by default** and covers both layers at once; there is no
  longer an intermediate "tools only" mode. ``-disable-sandbox`` turns both
  layers off.
- ``caged`` is now **required** (a missing ``caged``, or one without
  ``--report-fd``, is a hard startup error — never a silent fallback).
  ``caged`` v0.5.0 or newer is required.
- A sandbox setup failure is detected **only** by the descriptor ``caged``
  reports through ``--report-fd``, never guessed from the exit status (which
  the command shares with ``caged``). A command exiting ``2`` is now just that
  command's status.
- ``hooks.RunContext`` takes a context (hooks are cancellable) and an optional
  sandbox; ``hooks.Run`` is now a wrapper around it with
  ``context.Background()``.
- Cancelling a turn kills the whole process group of a command, so grandchildren
  of ``bash -c`` (e.g. ``sleep 100 &``) do not survive.
- ``~/.llm-bridge`` (context files and global hooks) and ``/dev/urandom`` are
  read-only in the default policy, so context, hooks and TLS keep working
  confined.
- No project directory is granted by default: whoever starts the bridge decides
  which trees are visible, via ``-sandbox-ro``/``-sandbox-rw``.

v0.4.0
------

Changed
~~~~~~~

- **The bridge now runs the tools itself** (previously the client executed
  them). Read-only tools (``read``, ``grep``, ``glob``, ``knowledge``) run
  immediately, in the same turn, with no client round-trip and no confirmation.
  Mutating tools (``shell``, ``write``, ``search_replace``) are offered to the
  client for approval via a new ``tool_confirm`` **event**; the client approves
  with a new ``tool_confirm`` **command** and the bridge then runs the tool. To
  deny a tool the client sends ``cancel`` as before.

  - New event ``tool_confirm`` (mutating tools awaiting approval). The
    ``tool_call`` event now carries only read-only tools that were already run
    by the bridge (display only).
  - New command ``tool_confirm`` (``{"method":"tool_confirm","params":{"id":...}}``)
    approving a pending mutating tool.
  - The ``tool_result`` command is now a **no-op** (kept for backwards
    compatibility); the bridge no longer waits on the client to execute tools.
  - ``history.Sanitize`` also drops orphaned ``tool`` messages, so cancelling a
    turn after only the read-only tools ran leaves a valid history.

Added
~~~~~

- ``ReadOnly`` classification on the tool definitions (``llm.Tool``) and a
  ``tools.IsReadOnly`` helper; the bridge implementation of the tools
  (``tools/exec.go``).

v0.3.0
------

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
