Knowledge base internals
========================

The ``knowledge`` package (``knowledge/``) is split into small pieces:

``index.go``
    The vector store. ``Index`` is an interface over a **brute-force** cosine
    index (``NewBruteForce``): vectors are L2-normalized on insert, so cosine
    similarity equals the dot product. ``Search`` returns the top-k hits
    (default 5) by descending score, ties broken by insertion order. The
    interface exists so a fancier structure (e.g. HNSW) can be swapped in
    later — the brute-force version is enough for the expected scale.

``embed.go``
    The ``Embedder`` interface, the pluggable point for embedding providers. It
    is deliberately prefix-free: the E5 ``query: ``/``passage: `` prefixes
    (``QueryPrefix`` / ``PassagePrefix``) are applied by the caller, not by the
    embedder.

``embed_onnx.go``
    The real ONNX-backed embedder, compiled **only** with the ``knowledge_onnx``
    build tag. It loads a ``multilingual-e5-small``-style model
    (``tokenizer.json`` + ``model.onnx``) and runs the standard
    sentence-transformers pipeline: tokenize → forward → mean pooling → L2
    normalize. Documents longer than 512 tokens are truncated (the head is
    embedded).

``embed_disabled*.go``
    A stub embedder (``DisabledEmbedder``) used when the ONNX model is not
    available or the build lacks the ``knowledge_onnx`` tag. It reports that
    embeddings are disabled, so the KB degrades gracefully instead of crashing.

``lazy_embed.go``
    ``LazyEmbedder`` defers loading the (expensive) ONNX model until the first
    ``Embed`` call, guarded by ``sync.Once``. The bridge boots fast and a failed
    load surfaces as the result of the first ``knowledge`` operation rather
    than a startup crash; a disabled KB is indistinguishable from a slow one
    until the model is actually used.

``manager.go``
    ``Manager`` ties an embedder, an index and on-disk persistence together,
    scoped to a single knowledge base (a *key* under the base directory).

``set.go``
    ``Set`` is the surface the ``knowledge`` tool talks to: it bundles one or
    two scoped managers (the shared ``global`` KB and/or the current
    ``project`` KB) and routes reads/writes over them.

``seed.go``
    ``EnsureSeeded`` populates a KB from curated ``.md`` seed files the first
    time it is used.

The ``Manager``
---------------

``knowledge.Manager`` is the core type. The ``knowledge`` tool's ``command``
field dispatches to it:

- ``show`` — lists the stored labels (a human-readable summary).
- ``search <query>`` — embeds the query (with the ``query:`` prefix) and
  returns the top-k matching notes with their scores.
- ``add <label> <text>`` — embeds the text (with the ``passage:`` prefix) and
  stores it under ``label``. It is an **upsert by label**: re-adding an
  existing label replaces its text and vector instead of inserting a
  duplicate. The label is the stable identifier the model uses both to store a
  note and to correct one it previously wrote.
- ``delete <label>`` — removes the item with that label.
- ``reset`` — clears the KB and rebuilds it from the seed (or simply wipes it
  when no seed root is configured).

The ``Set``
-----------

``knowledge.Set`` wraps one or two ``Manager``s, each tagged with a scope name.
Which ones it holds follows the effective scope (``global`` — the default —,
``project`` or ``both``):

- ``show`` and ``search`` cover **every** scope in the set. A search is run
  against each manager, the hits are merged by descending score and truncated
  to ``limit`` (default 5); with more than one scope the labels are prefixed
  with the scope name (``global: …`` / ``project: …``) so the model can tell
  where a note came from.
- ``add``, ``delete`` and ``reset`` target exactly **one** manager: the scope
  named in the tool's optional ``scope`` argument, or the default scope when it
  is omitted. The default is the only scope in single-scope configurations and
  ``global`` in the ``both`` mode, so notes are shared unless the caller
  explicitly asks for the project KB. Naming a scope that is not active is an
  error with two scopes and a plain note with one.

Persistence
-----------

When a base directory is set, the manager persists to
``<baseDir>/<key>/data.json`` after every change (``save``). The key is the
project name (last element of the cwd) for a project KB and ``_global`` for the
shared one. The on-disk schema is the simple ``{id, payload: {label, text},
vector}`` shape. On load, the index is rebuilt from the persisted vectors, so
the KB survives restarts without re-embedding.

Seeding
-------

``EnsureSeeded`` populates the KB the first time it is used, so it starts with
curated content instead of being empty. Global seeds under ``seedRoot/*.md``
are combined with project seeds under ``seedRoot/<seedProject>/*.md``. The
manager's storage key and its *seed project* are decoupled
(``SetSeedProject``): a project KB uses its project name for both, while the
shared KB (stored under ``_global``) has an empty seed project and therefore
takes only the global seed files — there is no ``_global`` seed subdirectory.
Each file is stored with a label derived from its path relative to
``seedRoot`` (prefixed with ``seed:``), so global and per-project files with
the same basename never collide. It is idempotent — once the index has items,
nothing is embedded again.

Wiring
------

``cmd/bridge`` resolves the base directory and the embedder and passes a
``-kb-scope`` (``global`` by default, or ``project``/``both``) to
``server.RunWithKnowledgeBase``. The server builds the ``Set`` for the
effective scope in ``loadKB``: on ``set_cwd`` and whenever a prompt changes the
scope via the ``kb_scope`` override (which, like the model/provider overrides,
persists between prompts). In the ``global`` scope a ``set_cwd`` leaves the KB
untouched — the shared KB does not follow the cwd; in ``project``/``both`` the
cwd project's ``data.json`` is loaded. Clearing/disabling the KB clears the
set when it is disabled. The ``knowledge`` tool calls are resolved **locally**
inside ``runToolCycle``, so they never wait on the client. A one-line note
about the available KB(s) is injected on the first turn when the KB is enabled;
that same first turn is where the context entries and the KB note are appended
to the top of the history (see :doc:`architecture`).
