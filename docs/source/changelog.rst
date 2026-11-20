Changelog
=========

Unreleased
----------

Changed
~~~~~~~

- An MCP call runs in the directory of the prompt, like every other tool. A
  server started from the bridge's own directory saw no repository and
  answered ``fatal: not a git repository``. The ``cwd`` of the server config
  still wins when it is set, so a server pinned to a directory stays there.

Added
~~~~~

- **A writable git directory for the server that runs git.** The bridge mounts
  the ``.git`` of
  every repository under the read-write roots read-only, so no tool edits hooks
  or config behind your back. A server that runs ``git add`` or ``git commit``
  needs it writable: staging writes ``.git/index`` and committing writes
  objects and refs. Set ``writable_git`` to ``true`` in that server's entry and
  it gets the mount without the protection. The cost is that the server can
  write any ``.git`` under those roots. See :doc:`mcp`.

v0.10.0
-------

The bridge runs local commands that speak the MCP tool protocol and offers
their tools to the model, with the same sandbox and the same confirmation step
as the built-in ones.

Added
~~~~~

- **MCP servers as tools.** List your commands in
  ``~/.config/llm-bridge/mcp.json`` and the bridge offers their tools as
  ``mcp__<server>__<tool>``. Each call runs the command in its own process,
  under the policy the config asks for: read-only and read-write paths, private
  ``/tmp`` and ``/dev/shm``, and no network unless the entry says otherwise.
  Tools the config does not list as ``read_only`` go to the client as
  ``tool_confirm`` first. ``-mcp-config`` names another file and ``-mcp=false``
  turns the feature off. A server that fails to answer at startup is left out
  with a warning in the log file. See :doc:`mcp`.
- **The tool_output event.** A tool that answers in pieces streams each one to
  the client while it runs; MCP calls are the only ones today. The history gets
  the joined text when the call ends, so a client that ignores the event loses
  the live display and nothing else. A call stopped by a limit keeps what
  arrived and a line saying why it stopped. See :doc:`protocol`.

v0.9.0
------

Added
~~~~~

- **OpenAI provider.** Use ``--provider openai`` or set ``provider`` to
  ``openai`` in a prompt. The bridge streams replies and tool calls through
  the Responses API with ``store: false``. Reasoning summaries are off by
  default. See :doc:`hacking/providers` for configuration and API details.
- **Six OpenAI models are in the catalog.** ``gpt-6.2-astra``,
  ``gpt-6.2-sol`` (the default), ``gpt-6.1-terra``, ``gpt-6-luna``,
  ``gpt-6.1-sol`` and ``gpt-6-sol``, all with a 1050000 token window and a tier
  above 272000 tokens. The first four carry their price, so ``turn_end``
  reports a cost for them. The last two have no price, and a turn on them
  reports ``cost_usd: null``.

Changed
~~~~~~~

- The README and documentation home page now list the main features,
  including image inputs and sandboxed tools.

v0.8.0
------

A turn now reports its cost and its time, and the model catalog with the prices
is a JSON file you can replace without a rebuild. Each model can have its own
system prompt too.

Added
~~~~~

- **The ready event announces the providers and their models.** It carries
  ``default_provider`` and a ``providers`` list, each entry with the model in
  use now, the ids the provider answers for with their context window, and how
  it takes the reasoning effort (the levels it accepts and whether a token
  budget works instead). A client offers the real names instead of keeping its
  own copy of them, and a model added to the bridge shows up without a client
  release. The deepseek aliases (``deepseek-chat``,
  ``deepseek-reasoner``) are left out. A bridge with no provider registry still
  sends the bare ``{"event":"ready"}``.
- **Model data is a JSON file.** The context windows, the model ids and the
  reasoning controls of each provider moved out of Go tables into
  ``llm/models.json``, embedded in the binary. Save a file as
  ``~/.config/llm-bridge/models.json`` to replace it without a rebuild. The
  file replaces the embedded data as a whole, and an invalid one is logged and
  ignored. The format is in :ref:`providers-context-window`.
- **Gemini 4 Argon and Hydrogen are in the catalog.** Both take a 1M token
  prompt. Gemini 4 Argon doubles its price on 2026-12-01, so the rates in
  ``llm/models.json`` need an edit then (see :ref:`providers-price`).
- **The turn_end event reports the cost.** ``cost_usd`` is the cost of the turn and
  ``session_cost_usd`` the total since the bridge started. The prices are in
  ``llm/models.json`` next to the window. Each provider works out the cost of a
  call with its own rules and sets ``Usage.CostUSD``: Anthropic bills cache
  writes apart, Gemini counts the thinking tokens as output and DeepSeek has
  peak and off-peak rates. Haiku 5.5 and Gemini 3.1 Pro have a tier for long
  prompts. A model without a price gives ``null``. The format is in
  :ref:`providers-price`.
- **The turn_end event reports the time of the turn.** ``duration_ms`` is the wall
  time from the prompt to the event, ``llm_ms`` sums the provider calls and
  ``tool_ms`` sums the tool runs. The wait for a ``tool_confirm`` is in
  ``duration_ms`` only. A cancelled turn has no ``turn_end``, so it has no
  timing.
- **System prompts per model.** The embedded prompt moved from
  ``server/system_prompt.md`` to ``server/prompts/default.md``, and a file named
  after a model, a model prefix or a provider replaces it for those models. A
  model prompt can hold ``{{base}}`` to include the default. Files in
  ``~/.config/llm-bridge/prompts/`` win over the embedded ones and are read on
  every request. The ``system`` field and ``-system-prompt`` still win over
  both. The lookup is in :ref:`providers-system-prompts`.
- **Anthropic requests ask for prompt caching.** Three ``cache_control``
  breakpoints go out on every request: the last tool of the list, the system
  prompt and the last block of the last message. The tool breakpoint caches the
  tool list, the system breakpoint caches the tool list plus the prompt, and the
  message breakpoint covers the tool calls of the running turn. ``system``
  became a block list, because the API only takes ``cache_control`` on a block.

  ``-prune`` and ``-aggressive-prune`` rewrite the middle of the history, so the
  message breakpoint misses on the first request after a pruned turn and the
  prefix is written again at 1.25x. The tool and system breakpoints sit at the
  front of the prefix and keep hitting.

Changed
~~~~~~~

- **The deep-v4-pro model id is now deepseek-v4-pro.** The DeepSeek docs
  name the model ``deepseek-v4-pro``, so the old id was a typo. A client that
  kept ``deep-v4-pro`` from an earlier ``ready`` event gets the provider's model
  error. The two aliases, ``deepseek-chat`` and ``deepseek-reasoner``, still
  work.

v0.7.0
------

The bridge no longer re-executes itself under caged. Every process it starts
gets exactly one policy: the bridge runs unconfined, and each tool is a caged
child with the policy built for it.

Why: the "ceiling" made the bridge a privileged process, and — because Landlock
and seccomp are inherited — every external process it spawned ran under two
stacked policies. That is what blocks giving a tool its own namespace: caged's
namespace support has to write ``/proc/self/uid_map`` during setup, and from
inside a ceiling that file cannot be granted (a Landlock rule is per inode, and
the grant resolves to the ceiling's own ``uid_map``) nor opened to the model
(``/proc/<pid>/environ`` of the bridge carries the API keys). One policy per
process is what makes namespaces possible at all.

The trade is explicit: the bridge itself is no longer bounded by the kernel. The
invariant that keeps that honest is that **no tool may open a path the model
chose inside the bridge process** — which is why the four filesystem tools moved
to ``llm-tools`` in the same release.

Breaking changes
~~~~~~~~~~~~~~~~

- **Every tool call gets private namespaces.** ``/tmp`` and ``/dev/shm`` are
  fresh tmpfs mounts inside the call, and the network stack has loopback up and
  no route out. ``-sandbox-loopback-only`` is gone, because the namespace is no
  longer optional: a tool that needs a local server reaches it in the same
  ``shell`` invocation, and a name outside ``127.0.0.0/8`` does not resolve.
- **The -sandbox-tmp flag changed meaning.** It was a dedicated directory granted
  read-write to the tools; it is now the host directory bind mounted over the
  private ``/tmp``. The default is empty, which is a fresh tmpfs. ``TMPDIR``,
  ``GOTMPDIR``, ``TMP`` and ``TEMP`` name ``/tmp`` in either mode, so the model
  stops writing to a path the policy does not grant.
- ``cmd/bridge`` is now ``cmd/llm-bridge``, and ``make build`` produces two
  binaries: ``llm-bridge`` and ``llm-tools``. Both go to ``build/`` and both
  must be installed together; the bridge looks for ``llm-tools`` next to itself
  first, then in ``$PATH`` (override with ``-tools-bin``).
- A missing or mismatched ``llm-tools`` is a **startup error**, like a missing
  ``caged``: it answers a handshake before the sandbox is built, so a stale
  binary fails loudly instead of answering requests it does not understand.
  With ``-disable-sandbox`` there is no policy to run the helper under, so the
  filesystem tools stay in-process.
- The bridge is **no longer confined at startup**: ``LLM_BRIDGE_SANDBOXED`` and
  the re-exec are gone, ``-logfile`` is opened by the bridge itself (there is no
  longer a policy that has to grant it before the fact), and the log file is not
  part of any tool policy. A launch under an outer sandbox (the client wrapping
  the bridge in caged) still works, but the tools are then nested under that
  outer policy — the two layers are no longer managed by the bridge.

Changed
~~~~~~~

- **Anthropic is a provider.** ``--provider anthropic`` (or a prompt's
  ``provider: "anthropic"``) talks to ``POST /v1/messages`` with streaming: the
  API key in ``x-api-key``, the fixed ``anthropic-version`` header, and the
  ``max_tokens`` the API requires. Model defaults to ``claude-sonnet-5-5``,
  ``max_tokens`` to ``8192``. The provider maps the three shapes this API has
  that the others do not: an assistant message is a list of content blocks
  (thinking, text, ``tool_use``), a tool result is a ``tool_result`` block
  inside a user message with the consecutive results sharing one message, and
  thinking is ``adaptive`` by default or ``enabled`` with a
  ``thinking.budget_tokens``. ``output_config.effort`` carries the level.
- **The provider interface carries only what providers have in common.**
  ``ToolCall.ThoughtSignature`` became ``ToolCall.Signature``, and a matching
  ``Message.Signature`` covers the advice Anthropic signs instead (it rejects a
  replayed thinking block without it). ``Usage`` lost its DeepSeek-shaped
  ``CacheHitTokens``/``CacheMissTokens`` and their JSON tags, which moved into a
  private ``openAIUsage``: the neutral pair is now ``PromptTokens`` and
  ``CachedTokens``, and the server derives the hit and miss counts that
  ``turn_end`` reports. ``ChatRequest.ReasoningEffort`` (a string whose meaning
  depended on which provider read it) became ``ReasoningLevel`` plus
  ``ThinkingBudget``: a level for DeepSeek and Anthropic, a number for Gemini.
  ``protocol`` still takes one ``reasoning_effort`` string and the server splits
  it, so no client changes.
- **stop_reason is one vocabulary.** ``end_turn``, ``tool_use`` and
  ``max_tokens`` come out the same whichever provider answered, instead of
  ``stop`` / ``END_TURN`` / ``STOP`` in the same conversation. A value outside
  the three (a Gemini ``SAFETY`` block, an Anthropic ``refusal``) goes out as
  the provider wrote it.
- **The image detail hint is gone.** It was DeepSeek's
  ``image_url.detail`` and only DeepSeek read it, so it did not belong in the
  shared ``Image``. No client set it, and the API default (``auto``) is what
  those calls already got. A client that still sends ``detail`` has it ignored.
- ``~/.llm-bridge`` is now granted **read-write** (it was read-only) and created
  at startup when missing, so a tool call can maintain the context files and add
  a hook. A ``write`` or ``search_replace`` into a ``.llm-bridge`` directory
  carries ``risk: high`` on the ``tool_confirm``, and so does a ``shell``
  command that names the directory: it steers every later session.
- The startup probe runs caged once with the mount and network namespaces, so a
  kernel or an LSM that refuses unprivileged user namespaces stops the bridge at
  startup with a message naming the reason, instead of failing every tool call.
- ``tools.Executor`` gained ``NewExecutorWithToolbox``; when a helper and a
  policy are both set, the four filesystem tools go through
  ``sandbox.Sandbox.RunIO`` (a new ``Run`` variant with stdin and stdout/stderr
  kept apart — a protocol response cannot share a pipe with a warning).
- ``tools.IsFileTool`` names the tools the helper serves. ``shell``, ``grep``
  and ``curl`` are deliberately not among them: they exec external programs, so
  they have to be the sandboxed command itself.
- **The removed flags are no longer stubs.** ``-sandbox-network`` and
  ``-sandbox-connect-port`` were kept parsed since v0.6.0 only to fail with a
  sentence pointing at ``-curl``/``-curl-port``; that shim is gone and the flags
  are simply not defined anymore, so an old configuration now gets the ``flag``
  package's own ``flag provided but not defined`` and exit status 2. The check
  also ran late (after the ``caged`` probes and the ``llm-tools`` handshake),
  which made the message look like a sandbox problem; ``flag`` rejects it while
  parsing, before anything is validated.
- ``scripts/sandbox-e2e.sh`` measures the namespaces against the real ``caged``
  binary, and ``TestFunctionalToolNamespaces`` covers the same ground from Go:
  the private ``/tmp`` and ``/dev/shm``, the temp variables, a server and a
  client in one call, loopback as the only interface, ``ENETUNREACH`` on an
  external address and ``-sandbox-tmp`` exchanging a file with the host. The
  test builds its sandbox through ``sandboxConfig.validate`` and ``toolSandbox``,
  so it measures the policy the bridge uses. The script exits non-zero on a
  skipped functional test, because a skip on a machine that cannot create a user
  namespace says nothing about the policy.

Fixed
~~~~~

- **The tools can read /dev/zero.** The tool policy granted ``/dev/urandom``
  read-only and ``/dev/null`` writable, and stopped there, so every ``dd
  if=/dev/zero``, ``head -c 1M /dev/zero`` or ``mkfs`` aimed at a file under the
  private ``/tmp`` died with ``EACCES`` on the input. The device hands out zeros,
  so there is nothing of the host in it. It is now in the read-only list next to
  ``/dev/urandom``.
- **The shell risk label no longer fires on ordinary commands.** The
  markers in ``tools.Risk`` were matched as plain substrings, and most of them
  are two or three letters long: the marker for ``nc`` matched inside ``func``
  and ``sync``, so ``go test -run TestFunc`` was reported as **high** risk, and
  ``| sh`` matched inside ``| shellcheck``. They are now pre-compiled regular
  expressions anchored on word boundaries (``\bnc\b``, ``\bsu\s+-``, …), flag
  clusters are written as ``-[a-z]*r`` so ``rm -rf``/``rm -fr``/``rm -rfv`` all
  match without enumerating orderings, and the pipe-to-shell shape is a single
  expression whose trailing ``\b`` keeps ``| shellcheck`` and ``| ssh`` out. The label
  stays advisory — it never blocks a call — but a false positive now costs
  nothing instead of drowning the real ones.

v0.6.0
------

Added
~~~~~

- **Confirmed writes for the knowledge base.** ``knowledge`` is now classified
  per call: ``show`` and ``search`` keep running inline, while ``add``,
  ``delete`` and ``reset`` go through ``tool_confirm``. Before this, a page read
  by the model could plant a note in the shared KB with no human in the loop —
  silently, and for every later session. A malformed or unknown command is
  treated as mutating.
- **What an approval really runs.** A ``tool_confirm`` for ``shell`` now carries
  ``segments`` (the commands the one-liner runs, split on ``;``, ``&&``,
  ``||``, ``|`` and newlines, never inside quotes), ``risk`` (``high`` for
  destructive/remote/privileged commands and pipes into a shell, ``medium`` for
  ordinary state changes, ``low`` otherwise) and ``tainted`` (the arguments
  reproduce content fetched earlier in the session by ``curl``). All three are
  optional and advisory: an older client ignores them. A one-liner with more
  than 10 segments is **not offered at all** — the bridge refuses it and tells
  the model to split the command.
- **Fetched content is framed as data.** What ``curl`` returns is truncated and
  then wrapped in ``<untrusted-web-content url="...">...</untrusted-web-content>``,
  and the system prompt now says that everything inside those tags is a page
  someone else wrote: an instruction found there is reported to the user, never
  followed, and fake framing (``<|im_start|>system``, ``### System:``,
  ``[INST]``, a faked ``<tool_result>``) is an attack.
- Every ``curl`` execution writes one audit line (``curl url=… bytes=… ok=…``)
  to the log file, so what left the machine can be reconstructed afterwards.
- New ``curl`` tool: fetch an http/https URL, with a small allowlist of options
  (``-H``, ``-L``, ``-I``, ``-i``, ``--compressed``, ``-A``, ``-b``,
  ``--max-time``, ``--connect-timeout``, ``--max-redirs``, ``--max-filesize``).
  It is the **only** tool with network access and it is **mutating**, so every
  call goes through ``tool_confirm``. It runs under its **own** caged policy that
  grants the system trees and nothing else — no projects, no home, no knowledge
  base, no temp directory — so a fetch cannot read a local file and therefore
  cannot exfiltrate one through a URL, a header or a DNS query. Output is
  truncated to 64 KiB.
- Flags: ``-curl`` (default ``true``), ``-curl-port`` (repeatable, default 443
  and 80), ``-curl-any-port`` and ``-curl-allow-host`` (repeatable; the host
  filter is enforced by the bridge, since Landlock has no host rules).
- **The -sandbox-loopback-only flag.** The tools run in their own network namespace
  with loopback up and no route anywhere else, so a tool can start a server and
  talk to it in the same command (a test suite against a local port) and nothing
  it does leaves the machine: an external address does not exist in there, so
  the connection fails with ``ENETUNREACH`` and DNS does not resolve. The bound
  is the namespace, not a rule — which is why the socket families and any-port
  connect/bind are released along with it. Each tool call gets a fresh
  namespace: a server started by one call is not reachable from the next. It is
  checked at startup by running caged once, so a kernel or LSM that refuses
  unprivileged user namespaces (or a bridge running inside another cage) is a
  startup error instead of a sandbox failure on every tool call.
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

- ``IsReadOnly("knowledge")`` is now ``false``: the tool can write, so it is
  classified as mutating and the per-call decision moved to
  ``tools.MutatesCall``.
- **Network access is no longer a property of the tools in general.** The
  ``-sandbox-network`` and ``-sandbox-connect-port`` flags were **removed** (an
  old configuration fails at startup, pointing at the replacements): the
  ``shell`` and ``grep`` tools and the hooks never have the network, and the new
  ``curl`` tool always has it under its own, much narrower filesystem policy.
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
- The ``knowledge`` tool schema documents the ``scope`` argument.
- Docs and the system prompt describe the shared KB as the default.

Fixed
~~~~~

- **Redirections are no longer command separators.** ``Segments`` treated the
  ``&`` of ``2>&1``, ``>&2`` and ``<&3`` as a separator, so
  ``git describe --tags 2>&1 || true`` was reported as four commands
  (``git describe --tags 2>``, ``1``, ``true``) instead of two — inflating the
  approval header, the class key a client derives from it, and the ten-segment
  limit. ``>``/``<`` now consume the following ``&``, ``&>`` and ``&>>`` are
  redirects as well, and ``|&`` is a single separator. Heredoc bodies are still
  not parsed.

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
