Usage
=====

.. note::

   Currently only deepseek and google gemini are implemented as providers
   in the bridge. In future releases other providers may be implemented



This page covers how to run the bridge and every way of calling and using it:
as a stdio subprocess, through its CLI flags, through prompts and hooks, and in
the standalone knowledge-base mode.

Running the bridge
------------------

The bridge is a **stdio** process: it reads JSON-lines commands from stdin and
writes JSON-lines events to stdout. It does not open a network port; a client
(e.g. Emacs) spawns it as a subprocess and talks to it over its pipes. Because
of that, the bridge runs on the host and the model's tools operate directly on
the project directory.

.. code-block:: sh

   $ ./build/llm-bridge [flags]

Once it is up it emits a single ``ready`` event, and only then should the client
start sending commands.

Because stdout is reserved for the protocol, logs must go to a file or be
disabled, or they will corrupt the client stream:

.. code-block:: sh

   $ ./build/llm-bridge -logfile /tmp/llm-bridge.log

The full wire format (commands and events) is described in :doc:`protocol`.

Ways of using the bridge
------------------------

Once running, there are four ways to interact with the bridge:

**1. Prompts (the LLM path).** Send a ``prompt`` command and the bridge forwards
it to the active provider, streaming the answer back as ``chunk`` /
``thinking`` events (and ``tool_call`` when the model wants a tool). A prompt
can also carry **overrides** — ``provider``, ``model``, ``thinking``,
``reasoning_effort`` and ``system`` / ``system_prompt`` — which, once set,
persist across subsequent prompts. This is how a single session can switch
provider or change the model on the fly.

.. code-block:: json

   {"method": "prompt", "params": {"text": "add a test for parseHook"}}

A prompt can also carry **images** via the ``images`` array, given by local
``path``, external ``url`` or inline base64 ``data`` (the last is what a client
uses for a clipboard paste). See :ref:`Images <Images>` in :doc:`protocol` for
the field-by-field details and provider support.

.. code-block:: json

   {"method": "prompt", "params": {"text": "what is in this?", "images": [{"path": "screenshot.png"}]}}

**2. Hooks.** A prompt whose text starts with ``#`` is **not** sent to the LLM;
it runs a local script instead. See :doc:`hooks`.

.. code-block:: json

   {"method": "prompt", "params": {"text": "#algo foo bar"}}

**3. Tool approval and control commands.** The bridge runs the tools itself.
Read-only tools (``read``, ``grep``, ``glob``) run without asking; for a
mutating tool (``shell``, ``write``, ``search_replace``, ``curl``) the bridge emits a
``tool_confirm`` event and the client replies with a ``tool_confirm`` command to
approve it (or ``cancel`` to abort). The client can also ``cancel`` an
in-flight turn, ``set_cwd`` to move the working directory (which also reloads the
knowledge base in the project/both scopes), ``set_knowledge_bases`` to choose
the active bases, and ``quit`` to shut down.

**4. Standalone knowledge-base population.** A mode that does not run the
server at all — it fully rebuilds a knowledge base from a seed directory and
exits (see :doc:`knowledge`):

.. code-block:: sh

   $ ./build/llm-bridge -populate-project-kb ~/.local/share/llm-bridge/seeds -project <project>

CLI flags
---------

``--provider <name>``
    The default/initial LLM provider: ``deepseek`` (default) or ``google``.
    Both providers are registered up front, so a prompt can still switch the
    active provider per request via the ``provider`` override.

``--model <name>``
    Force a model. When it is the default provider it overrides
    ``DEEPSEEK_MODEL`` / ``GOOGLE_MODEL``. For DeepSeek without any explicit
    model, the model is derived from the thinking mode
    (``deepseek-flash`` / ``deepseek-chat``).

``--thinking[=true|false]``
    Enable/disable thinking mode (default ``true``). When no explicit model is
    set for DeepSeek, this also picks the default model. Honored per request by
    the ``thinking`` override.

``--reasoning-effort <value>``
    When thinking is on: a reasoning effort for DeepSeek (``low``/``medium``/
    ``high``, default ``high``) or a numeric thinking budget for Google.

``--system-prompt <path-or-text>``
    Override the system prompt. If the value is a path to an existing file its
    contents are used, otherwise it is treated as a literal system prompt
    string.

``-prune``
    Collapse reasoning and tool results of file tools into user snapshots
    (``<State>`` / ``<Replace>``), preserving file states while dropping the
    intermediate tool calls and chain-of-thought from the history. Saves tokens
    and keeps the prefix cacheable.

``--aggressive-prune``
    Collapse each completed tool-calling turn into just the user prompt plus
    the final answer, dropping the intermediate tool calls, tool results and
    chain-of-thought entirely. Mutually exclusive with ``-prune``.

``--knowledge[=true|false]``
    Enable/disable the knowledge base and its ``knowledge`` tool (default
    ``true``).

``--knowledge-base <dir>``
    Base directory for the knowledge bases (default
    ``~/.local/share/llm-bridge/knowledge_bases``; ``KNOWLEDGE_BASE`` env also
    honored).

``--kb-scope <global|project|both>``
    Which knowledge base(s) to use: ``global`` (default — one KB shared across
    every project, stored under ``_global``), ``project`` (one KB per cwd
    project, keyed by the last path element of the cwd) or ``both`` (the shared
    KB plus the project's; searches merge both and ``add`` defaults to the
    global KB unless ``scope:"project"`` is given). A prompt can override the
    scope for the rest of the conversation with ``kb_scope``.

``--kb-load <lazy|eager>``
    When to load the (heavy) ONNX KB model: ``lazy`` (default — loads on first
    use so the server starts fast; a failure surfaces on that first
    ``knowledge`` operation) or ``eager`` (loads at startup; a failure disables
    the KB with a warning).

``-logfile <path>``
    Redirect all logs to a file (and enable trace level). Keeps the JSON-lines
    stdout clean.

``-debug``
    Compatibility flag: same as ``-logfile`` with the default
    ``/tmp/llm-bridge.log``.

``-populate-project-kb <seedDir> -project <project>``
    Standalone mode (does not run the server): fully rebuild a knowledge base
    from a seed directory and exit. Requires the ONNX embedder
    (``make build-kb``). Use ``-project _global`` for the shared KB.

Sandbox (caged)
---------------

The bridge confines itself and the tools it runs with `caged
<https://docs.poraodojuca.dev/caged>`_, a small Landlock + seccomp sandbox. The
sandbox is **on by default** and has
two layers; Landlock rulesets stack by **intersection**, so the inner one can
only tighten the outer:

- **ceiling** — the bridge **re-executes itself** under caged at startup. The
  ceiling grants the system trees, the bridge and caged binaries, the
  knowledge-base locations (``~/.cache/llm-bridge`` read-only,
  ``~/.local/share/llm-bridge`` writable), the log file (that file only, never
  its directory) and network access (the bridge must reach the LLM API). It
  does **not** grant the projects: where they live is the caller's business, so
  the directories the bridge will work on have to be passed with
  ``-sandbox-rw`` (the ceiling is frozen at startup while the project changes at
  runtime via ``set_cwd``). When the bridge is already sandboxed at launch (the
  client started it through caged), the re-exec is skipped, so the layers do not
  duplicate.
- **tools** — every **external process** the bridge spawns (the ``shell`` and
  ``grep`` tools and the hooks) runs under caged with the **same filesystem
  policy** as the bridge (minus the log file), so a tool works on the same
  projects and knowledge base the bridge does. What a tool is denied, then, is
  the environment (no API keys: only caged's small base set is inherited), the
  IPC (unix sockets and SysV IPC) and the network. The in-process tools
  (``read``, ``write``, ``glob``, ``search_replace``) do not exec anything and
  are therefore only bounded by the bridge's own (ceiling) permissions.
- **curl** — the one tool with network access, and the only one that does
  **not** get the shared filesystem policy. It runs under its own caged policy
  with the system trees and nothing else (no projects, no home, no knowledge
  base, no temp directory), because network plus "can read the user's files" is
  an exfiltration primitive: the answer can leave inside a URL path, a header or
  a DNS query. With nothing to read, the only data left is the conversation the
  model already has. The destination is bounded by port (see ``-curl-port``) and,
  optionally, by host (``-curl-allow-host``).

.. note::

   ``-sandbox-network`` and ``-sandbox-connect-port`` were **removed**: network
   access is not a property of the tools in general anymore, only of the curl
   tool. A configuration that still passes them fails at startup with a message
   pointing at ``-curl``/``-curl-port``, instead of silently running without the
   network it asked for.

Because the sandbox is on by default, ``caged`` must be available (see its
`documentation <https://docs.poraodojuca.dev/caged>`_ for building and
installing it): a missing binary is a **startup error**, never a silent
fallback to unsandboxed execution.

``--disable-sandbox``
    Turn both layers off (nothing is sandboxed).

``--caged-bin <path>``
    Path of the `caged binary <https://docs.poraodojuca.dev/caged>`_. Defaults
    to ``$CAGED_BIN`` and then ``$PATH``.

``--sandbox-ro`` / ``--sandbox-rw`` (repeatable)
    Extra read-only / read-write paths, for the bridge **and** for the tools
    (they share the filesystem policy). The projects root belongs here: without
    ``-sandbox-rw ~/mysrc`` neither the bridge nor the tools can read or write
    any project (and the knowledge base of a project cannot be stored). On a Go
    project, ``-sandbox-rw ~/.cache/go-build`` (the ``GOCACHE``) and
    ``-sandbox-rw ~/go/pkg/mod`` (the module cache; writable only really needed
    for ``go get``) are what ``go build``/``go test`` need inside the sandbox.

``--curl`` (default ``true``)
    Expose the ``curl`` tool, the **only** tool with network access. It runs
    under its own caged policy that grants the system trees and nothing else —
    no project, no home directory, no knowledge base, no temp directory — so a
    fetch has no local file to read, and therefore nothing to leak into a URL, a
    header or a DNS query. It is a **mutating** tool (the client sees the URL and
    the options and confirms every call), and it only accepts a small allowlist
    of options: no uploads, no file writes, no config files. ``-curl=false``
    removes it from the tool list; ``-disable-sandbox`` does too, because it
    never runs outside its sandbox.

``--curl-port <port>`` (repeatable)
    TCP port the curl tool may connect to (default: ``443`` and ``80``). This is
    the kernel-level bound (Landlock ``NET_CONNECT_TCP``). It does not cover a
    UDP DNS query: with the socket families allowed, resolution still works, and
    a name lookup is not a channel the port rule can see.

``--curl-any-port``
    Let the curl tool connect to any TCP port (default: only the ports above).

``--curl-allow-host <host>`` (repeatable)
    Restrict the curl tool to these hosts; a leading dot (or ``*.``) also matches
    any subdomain. Default: any host. Landlock cannot filter hosts, so this is
    enforced by the bridge: it compares ``url.Hostname()`` before running curl.

``--sandbox-tmp <dir>``
    Temporary directory granted read-write to the bridge **and** the tools, and
    exported to them as ``TMPDIR``, ``GOTMPDIR``, ``TMP`` and ``TEMP`` (default
    ``/tmp/llm-bridge``). It is created on startup (mode ``0700``), before the
    sandbox is built — caged opens every granted path, so it has to exist.

    ``/tmp`` itself is **not** granted: a shared scratch directory would let any
    process read or replace another one's files. Pointing every
    temporary-directory convention at a private directory is what makes programs
    that need scratch space work under the sandbox: ``mkstemp``, ``gcc``,
    ``git``, and the Go toolchain (``go build``/``go test`` use ``GOTMPDIR`` for
    their work directory and ``TMPDIR`` for the rest), which otherwise fail with
    ``mkdir /tmp/go-build…: permission denied``.

    Non-empty values always win over the inherited variables: an exported
    ``TMPDIR=/tmp`` is replaced, since ``/tmp`` is not reachable. Pass
    ``-sandbox-tmp ""`` to leave the inherited variables alone (and grant no
    temp directory). Note that ``-sandbox-tmp /tmp`` grants the whole ``/tmp``.

A caged failure means *the sandbox did not apply* rather than *the command
failed*. The bridge learns that from caged's ``--report-fd``, a descriptor caged
writes its own diagnostics to and leaves empty when the sandbox was applied.
The exit status cannot be used for it: caged ``execvp``\s into the command, so
the two share the status space and a tool exiting ``2`` (an ``ls`` on a missing
file, say) or ``125`` looks exactly like a broken sandbox.

Such a failure is a hard error: it is logged and returned to the model as an
error, never as the tool's output, because the command did not run confined (or
did not run at all) and its status says nothing. For the same reason a ``caged``
without ``--report-fd`` is a **startup error** — there is no safe way to guess.

.. code-block:: sh

   $ ./build/llm-bridge -sandbox-rw "$HOME/mysrc" -logfile /tmp/llm-bridge.log
   $ ./build/llm-bridge -sandbox-rw "$HOME/mysrc" \
         -sandbox-rw "$HOME/.cache/go-build" \
         -sandbox-rw "$HOME/go/pkg/mod"
   $ ./build/llm-bridge -disable-sandbox -logfile /tmp/llm-bridge.log

Environment variables
---------------------

The bridge reads these from the environment at runtime.

**DeepSeek**

* ``DEEPSEEK_API_KEY`` — API key (required).
* ``DEEPSEEK_URL`` — API endpoint (default ``https://api.deepseek.com/chat/completions``).
* ``DEEPSEEK_MODEL`` — explicit model; when unset, derived from thinking (``deepseek-flash`` / ``deepseek-chat``).
* ``DEEPSEEK_THINKING`` — enable thinking mode (default ``true``).
* ``DEEPSEEK_REASONING_EFFORT`` — reasoning effort sent when thinking is on (default ``high``).

**Google / Gemini**

* ``GOOGLE_API_KEY`` — API key (required).
* ``GEMINI_API_KEY`` — alternative API key, used when ``GOOGLE_API_KEY`` is unset.
* ``GOOGLE_URL`` — API endpoint base (default ``https://generativelanguage.googleapis.com/v1beta``).
* ``GOOGLE_MODEL`` — explicit model; when unset, falls back to ``GEMINI_MODEL`` then ``gemini-3-flash``.
* ``GEMINI_MODEL`` — alternative explicit model, used when ``GOOGLE_MODEL`` is unset.
* ``GOOGLE_THINKING`` — enable thinking mode (default ``true``).
* ``GOOGLE_THINKING_BUDGET`` — thinking token budget when thinking is on (default ``1024``).

**Knowledge base**

* ``KNOWLEDGE_BASE`` — base directory for the KBs (default ``~/.local/share/llm-bridge/knowledge_bases``).
* ``LLM_BRIDGE_KB_MODEL`` — path to the ONNX model (default ``~/.cache/llm-bridge/model.onnx``).
* ``LLM_BRIDGE_KB_TOKENIZER`` — path to the tokenizer (default ``~/.cache/llm-bridge/tokenizer.json``).
* ``LLM_BRIDGE_ONNXRUNTIME_LIB`` — path to ``libonnxruntime.so``, required by the KB build.
* ``LLM_BRIDGE_KB_DIR`` — cache dir used by ``scripts/fetch_kb.sh`` to download the KB assets (default ``~/.cache/llm-bridge``; script only, not read by the bridge).

See :doc:`/hacking/providers` for the meaning of each provider variable and
:doc:`knowledge` for the KB ones.

Logging
-------

By default (no ``-logfile``/``-debug``) all logs are discarded, so stdout stays
reserved for the protocol. With ``-logfile <path>`` (or ``-debug``) the
logger switches to trace level and writes to that file.
