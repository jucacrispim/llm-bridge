Hacking
=======

Implementation details of the bridge internals. This is the place for how
things are built — the data structures, algorithms, wiring and gotchas. If you
just want to *use* a feature, see :doc:`context` and :doc:`knowledge`; this page
peeks under the hood.

Context loading
---------------

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

The project/general lookup mirrors how :doc:`hooks` scripts are resolved
(project first, then the user's home).

Knowledge base internals
------------------------

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
    scoped to a single project.

``seed.go``
    ``EnsureSeeded`` populates the KB from curated ``.md`` seed files the first
    time a project is used.

The ``Manager``
~~~~~~~~~~~~~~~

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

Persistence
~~~~~~~~~~~

When a base directory is set, the manager persists to
``<baseDir>/<project>/data.json`` after every change (``save``). The on-disk
schema is the simple ``{id, payload: {label, text}, vector}`` shape. On load,
the index is rebuilt from the persisted vectors, so the KB survives restarts
without re-embedding.

Seeding
~~~~~~~

``EnsureSeeded`` populates the KB the first time it is used for a project, so
it starts with curated content instead of being empty. Global seeds under
``seedRoot/*.md`` are combined with project seeds under
``seedRoot/<project>/*.md``. Each file is stored with a label derived from its
path relative to ``seedRoot`` (prefixed with ``seed:``), so global and
per-project files with the same basename never collide. It is idempotent —
once the index has items, nothing is embedded again.

Wiring
~~~~~~

The server builds the per-project ``Manager`` lazily on ``set_cwd`` from
``<baseDir>/<project>/data.json`` (clearing it when the KB is disabled), and
the ``knowledge`` tool calls are resolved **locally** inside ``runToolCycle``,
so they never wait on the client. A one-line note about the project's KB is
injected on the first turn when the KB is enabled; that same first turn is
where the context entries and the KB note are appended to the top of the
history (see :doc:`architecture`).
