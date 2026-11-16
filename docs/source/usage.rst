Usage
=====

.. note::

   Only deepseek and google gemini are providers today.

This page covers how you run the bridge and every way of calling it: as a stdio
subprocess, through its CLI flags, through prompts and hooks, and in the
standalone knowledge-base mode.

Running the bridge
------------------

The bridge is a **stdio** process: it reads JSON-lines commands from stdin and
writes JSON-lines events to stdout. It opens no network port. Your client (an
Emacs buffer, say) spawns it as a subprocess and talks to it over the pipes, so
the bridge runs on your host and the model's tools work directly on the project
directory.

.. code-block:: sh

   $ ./build/llm-bridge -sandbox-rw "$HOME/mysrc" -logfile /tmp/llm-bridge.log
   {"event":"ready"}

Wait for that line before you send the first command.

Stdout carries the protocol, so send the logs to a file or drop them. Anything
else on stdout corrupts your client's stream.

The full wire format (commands and events) is described in :doc:`protocol`.

Ways of using the bridge
------------------------

Once the bridge is up, you interact with it in four ways.

**1. Prompts.** Send a ``prompt`` and the bridge forwards it to the active
provider, streaming the answer back as ``chunk`` and ``thinking`` events, plus
``tool_call`` when the model wants a tool.

.. code-block:: json

   {"method": "prompt", "params": {"text": "add a test for parseHook"}}

A prompt can carry **overrides**: ``provider``, ``model``, ``thinking``,
``reasoning_effort`` and ``system`` or ``system_prompt``. An override persists
across the following prompts, so you can switch provider or change the model
mid-session.

A prompt can also carry **images**, by local ``path``, external ``url`` or
inline base64 ``data`` (what a client sends for a clipboard paste). The
field-by-field details are in :ref:`Images <Images>` in :doc:`protocol`.

.. code-block:: json

   {"method": "prompt", "params": {"text": "what is in this?", "images": [{"path": "screenshot.png"}]}}

**2. Hooks.** A prompt whose text starts with ``#`` does not go to the LLM: it
runs a local script instead. See :doc:`hooks`.

.. code-block:: json

   {"method": "prompt", "params": {"text": "#algo foo bar"}}

**3. Tool approval and control.** The bridge runs the tools itself. Read-only
tools (``read``, ``grep``, ``glob``) run without asking. For a mutating tool
(``shell``, ``write``, ``search_replace``, ``curl``) the bridge emits a
``tool_confirm`` event, and you reply with a ``tool_confirm`` command to approve
it or ``cancel`` to abort.

You can also send ``cancel`` to interrupt a turn in flight, ``set_cwd`` to move
the working directory (which reloads the knowledge base in the project and both
scopes), ``set_knowledge_bases`` to choose the active bases, and ``quit`` to
shut down.

**4. Standalone knowledge-base population.** A mode that runs no server: it
rebuilds a knowledge base from a seed directory and exits. See
:doc:`knowledge`.

.. code-block:: sh

   $ ./build/llm-bridge -populate-project-kb ~/.local/share/llm-bridge/seeds -project <project>

CLI flags
---------

``--provider <name>``
    The default provider: ``deepseek`` (default) or ``google``. Both are
    registered up front, so a prompt can still switch provider with the
    ``provider`` override.

``--model <name>``
    Force a model. For the default provider it overrides ``DEEPSEEK_MODEL`` /
    ``GOOGLE_MODEL``. DeepSeek with no explicit model derives it from the
    thinking mode (``deepseek-flash`` / ``deepseek-chat``).

``--thinking[=true|false]``
    Enable or disable thinking mode (default ``true``). On DeepSeek with no
    explicit model, this also picks the default model. A prompt can override it
    with ``thinking``.

``--reasoning-effort <value>``
    When thinking is on: a reasoning effort for DeepSeek (``low``, ``medium``,
    ``high``, default ``high``) or a numeric thinking budget for Google.

``--system-prompt <path-or-text>``
    Override the system prompt. When the value is the path of an existing file,
    its contents are the prompt. Otherwise the value is the prompt itself.

``-prune``
    Collapse the reasoning and the tool results of file tools into user
    snapshots (``<State>`` / ``<Replace>``). The file states stay in the
    history and the intermediate tool calls and chain-of-thought do not. Saves
    tokens and keeps the prefix cacheable.

``--aggressive-prune``
    Collapse each completed tool-calling turn into the user prompt plus the
    final answer, dropping the intermediate tool calls, tool results and
    chain-of-thought. Mutually exclusive with ``-prune``.

``--knowledge[=true|false]``
    Enable or disable the knowledge base and its ``knowledge`` tool (default
    ``true``).

``--knowledge-base <dir>``
    Base directory for the knowledge bases. Defaults to
    ``~/.local/share/llm-bridge/knowledge_bases``. ``KNOWLEDGE_BASE`` is
    honored too.

``--kb-scope <global|project|both>``
    Which knowledge base(s) to use. ``global`` (the default) is one KB shared
    across every project, stored under ``_global``. ``project`` is one KB per
    cwd project, keyed by the last path element of the cwd. ``both`` is the
    shared KB plus the project's: searches merge both, and ``add`` goes to the
    global one unless you pass ``scope:"project"``. A prompt can override the
    scope for the rest of the conversation with ``kb_scope``.

``--kb-load <lazy|eager>``
    When to load the ONNX KB model: ``lazy`` (default, loads on first use so the
    server starts fast, and a failure surfaces on that first ``knowledge``
    operation) or ``eager`` (loads at startup, and a failure disables the KB
    with a warning).

``-logfile <path>``
    Redirect all logs to a file and enable trace level. Keeps the JSON-lines
    stdout clean.

``-debug``
    Compatibility flag: the same as ``-logfile`` with the default
    ``/tmp/llm-bridge.log``.

``-populate-project-kb <seedDir> -project <project>``
    Standalone mode, runs no server: rebuild a knowledge base from a seed
    directory and exit. Needs the ONNX embedder (``make build-kb``). Use
    ``-project _global`` for the shared KB.

Sandbox (caged)
---------------

The bridge confines the processes it runs with `caged
<https://docs.poraodojuca.dev/caged>`_, a small Landlock + seccomp sandbox. The
sandbox is **on by default**, and every process gets **exactly one policy**: the
bridge itself runs unconfined, and each tool it spawns is a caged child with the
policy built for it.

.. note::

   Up to v0.7.0 the bridge also re-executed itself under a loose "ceiling"
   policy. That layer is gone. :doc:`changelog` has the reasoning.

Here is what the bridge spawns and the policy each one gets.

- **tools** - every external process the bridge runs (the ``shell`` and ``grep``
  tools and the hooks) runs under caged with the system trees, the
  knowledge-base locations (``~/.cache/llm-bridge`` read-only,
  ``~/.local/share/llm-bridge`` writable) and whatever ``-sandbox-ro`` and
  ``-sandbox-rw`` added.

  The projects are **not** granted by default: where they live is your business,
  so you pass the directories the bridge will work on with ``-sandbox-rw``. The
  policy is frozen at startup while the project changes at runtime via
  ``set_cwd``.

  A tool is denied the environment (no API keys: only caged's small base set is
  inherited), the IPC (unix sockets and SysV IPC) and the network. The exception
  is ``-sandbox-loopback-only``, which puts the tool in a private network
  namespace whose only interface is loopback (see below).

  The filesystem tools (``read``, ``write``, ``glob``, ``search_replace``) run
  in the **llm-tools** helper under this same policy, not inside the bridge, so
  the path the model asked for never reaches a syscall in the bridge process.
  ``llm-tools`` is a second binary next to the bridge (override with
  ``-tools-bin``), and a missing one is a startup error, never a silent fallback
  to running the tools in-process.
- **curl** - the one tool with network access, and the only one that does **not**
  get the shared filesystem policy. It runs under its own caged policy with the
  system trees and nothing else: no projects, no home, no knowledge base, no
  temp directory.

  Network plus "can read your files" is an exfiltration primitive. The answer
  can leave inside a URL path, a header or a DNS query, and no flag allowlist
  changes that. With nothing to read, the only data left is the conversation the
  model already has. The destination is bounded by port (see ``-curl-port``) and
  optionally by host (``-curl-allow-host``).

.. note::

   ``-sandbox-network`` and ``-sandbox-connect-port`` no longer exist: network
   access belongs to the ``curl`` tool alone. Use ``-curl`` and ``-curl-port``.
   See :doc:`changelog` for the removal.

The sandbox is on by default, so ``caged`` has to be installed. Its
`documentation <https://docs.poraodojuca.dev/caged>`_ covers building and
installing it. A missing binary is a **startup error**, never a silent fallback
to unsandboxed execution.

``--disable-sandbox``
    Turn the sandbox off, so no tool runs under caged.

``--caged-bin <path>``
    Path of the `caged binary <https://docs.poraodojuca.dev/caged>`_. Defaults
    to ``$CAGED_BIN`` and then ``$PATH``.

``--sandbox-ro`` / ``--sandbox-rw`` (repeatable)
    Extra read-only and read-write paths for the tool policies. Put your
    projects root here: without ``-sandbox-rw ~/mysrc`` the tools cannot read or
    write any project, and a project's knowledge base cannot be stored.

    On a Go project you also pass ``-sandbox-rw ~/.cache/go-build`` (the
    ``GOCACHE``) and ``-sandbox-rw ~/go/pkg/mod`` (the module cache, writable
    really only for ``go get``). Those are what ``go build`` and ``go test``
    need inside the sandbox.

``--curl`` (default ``true``)
    Expose the ``curl`` tool. It is a **mutating** tool, so your client sees the
    URL and the options and confirms every call, and it accepts only a small
    allowlist of options: no uploads, no file writes, no config files.
    ``-curl=false`` removes it from the tool list, and so does
    ``-disable-sandbox``, because it never runs outside its sandbox.

``--curl-port <port>`` (repeatable)
    TCP port the curl tool may connect to (default: ``443`` and ``80``). This is
    the kernel-level bound (Landlock ``NET_CONNECT_TCP``). It does not cover a
    UDP DNS query: with the socket families allowed, resolution still works, and
    a name lookup is not a channel the port rule can see.

``--curl-any-port``
    Let the curl tool connect to any TCP port (default: only the ports above).

``--curl-allow-host <host>`` (repeatable)
    Restrict the curl tool to these hosts. A leading dot (or ``*.``) also matches
    any subdomain. Default: any host. Landlock cannot filter hosts, so the bridge
    compares ``url.Hostname()`` before it runs curl.

``--sandbox-loopback-only``
    Run the tools in their own network namespace, with loopback up and no route
    anywhere else. A tool can then start a server and talk to it **in the same
    command** (a test suite against a local port), and nothing it does reaches
    the outside world: an external address does not exist in that namespace, so
    the connection fails with ``ENETUNREACH`` and DNS does not resolve.

    The bound is the namespace, not an address list. That is why the socket
    families and any-port connect/bind are released together with the flag:
    without them seccomp and Landlock would deny the socket before the namespace
    could matter. Landlock has no address field and seccomp cannot dereference
    the ``sockaddr``, so a namespace is the only way to say "localhost only".

    Each tool call gets its own namespace, so a server started by one call is not
    reachable from the next. Start the server and the client in the same
    ``shell`` invocation. The ``curl`` tool is unaffected, since a curl limited
    to loopback would fetch nothing.

    The flag is checked at **startup**, by running caged once with it. A kernel
    or an LSM that refuses unprivileged user namespaces (``AppArmor`` on Ubuntu
    23.10+, ``unprivileged_userns_clone=0``), a ``caged`` too old to know the
    flag, and a bridge that is itself running inside another cage all stop the
    bridge there. You get a startup error, never a sandbox failure on every tool
    call.

``--sandbox-tmp <dir>``
    Temporary directory granted read-write to the bridge **and** the tools, and
    exported to them as ``TMPDIR``, ``GOTMPDIR``, ``TMP`` and ``TEMP`` (default
    ``/tmp/llm-bridge``). It is created on startup with mode ``0700``, before the
    sandbox is built. caged opens every granted path, so it has to exist.

    ``/tmp`` itself is **not** granted: a shared scratch directory lets any
    process read or replace another one's files. A private directory is what
    programs that need scratch space get, and with it ``mkstemp``, ``gcc``,
    ``git`` and the Go toolchain work under the sandbox. The Go toolchain uses
    ``GOTMPDIR`` for its work directory and ``TMPDIR`` for the rest. Without them
    it fails with ``mkdir /tmp/go-build…: permission denied``.

    A non-empty value always wins over the inherited variables, so an exported
    ``TMPDIR=/tmp`` is replaced, since ``/tmp`` is not reachable. Pass
    ``-sandbox-tmp ""`` to leave the inherited variables alone and grant no temp
    directory. Note that ``-sandbox-tmp /tmp`` grants the whole ``/tmp``.

A caged failure means the sandbox did not apply, not that the command failed.
The bridge tells the two apart from caged's ``--report-fd`` descriptor, never
from the exit status, and returns the failure as an error for the model instead
of as the tool's output. The mechanism is in :doc:`sandbox`.

Three invocations you will use most:

.. code-block:: sh

   $ ./build/llm-bridge -sandbox-rw "$HOME/mysrc" -logfile /tmp/llm-bridge.log
   $ ./build/llm-bridge -sandbox-rw "$HOME/mysrc" \
         -sandbox-rw "$HOME/.cache/go-build" \
         -sandbox-rw "$HOME/go/pkg/mod"
   $ ./build/llm-bridge -disable-sandbox -logfile /tmp/llm-bridge.log

Environment variables
---------------------

Export these before you start the bridge.

**DeepSeek**

* ``DEEPSEEK_API_KEY`` - API key (required).
* ``DEEPSEEK_URL`` - API endpoint (default ``https://api.deepseek.com/chat/completions``).
* ``DEEPSEEK_MODEL`` - Explicit model. When unset, derived from thinking (``deepseek-flash`` / ``deepseek-chat``).
* ``DEEPSEEK_THINKING`` - enable thinking mode (default ``true``).
* ``DEEPSEEK_REASONING_EFFORT`` - reasoning effort sent when thinking is on (default ``high``).

**Google / Gemini**

* ``GOOGLE_API_KEY`` - API key (required).
* ``GEMINI_API_KEY`` - alternative API key, used when ``GOOGLE_API_KEY`` is unset.
* ``GOOGLE_URL`` - API endpoint base (default ``https://generativelanguage.googleapis.com/v1beta``).
* ``GOOGLE_MODEL`` - Explicit model. When unset, falls back to ``GEMINI_MODEL`` then ``gemini-3-flash``.
* ``GEMINI_MODEL`` - alternative explicit model, used when ``GOOGLE_MODEL`` is unset.
* ``GOOGLE_THINKING`` - enable thinking mode (default ``true``).
* ``GOOGLE_THINKING_BUDGET`` - thinking token budget when thinking is on (default ``1024``).

**Knowledge base**

* ``KNOWLEDGE_BASE`` - base directory for the KBs (default ``~/.local/share/llm-bridge/knowledge_bases``).
* ``LLM_BRIDGE_KB_MODEL`` - path to the ONNX model (default ``~/.cache/llm-bridge/model.onnx``).
* ``LLM_BRIDGE_KB_TOKENIZER`` - path to the tokenizer (default ``~/.cache/llm-bridge/tokenizer.json``).
* ``LLM_BRIDGE_ONNXRUNTIME_LIB`` - path to ``libonnxruntime.so``, required by the KB build.
* ``LLM_BRIDGE_KB_DIR`` - cache dir used by ``scripts/fetch_kb.sh`` to download the KB assets (default ``~/.cache/llm-bridge``). Used by the script only, never read by the bridge.

See :doc:`/hacking/providers` for what each provider variable means and
:doc:`knowledge` for the KB ones.

Logging
-------

By default, with neither ``-logfile`` nor ``-debug``, all logs are discarded and
stdout stays reserved for the protocol. Pass ``-logfile <path>`` (or ``-debug``)
and the logger switches to trace level and writes to that file.
