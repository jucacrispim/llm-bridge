Knowledge Base
==============

The bridge ships a small, local, per-project knowledge base (KB) that the model
can use during a conversation through the ``knowledge`` tool. It stores short
notes about what has been learned and lets the model retrieve them later with a
vector similarity search, scoped to the current working directory.

How it works
------------

The ``knowledge`` package (`knowledge/`) is split into small pieces:

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
    build tag. It loads a ``multilingual-e5-small``-style model (``tokenizer.json``
    + ``model.onnx``) and runs the standard sentence-transformers pipeline:
    tokenize → forward → mean pooling → L2 normalize. Documents longer than 512
    tokens are truncated (the head is embedded).

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
    scoped to a single project. See below.

``seed.go``
    ``EnsureSeeded`` populates the KB from curated ``.md`` seed files the first
    time a project is used. See *Seeding* below.

The ``Manager``
---------------

``knowledge.Manager`` is the core type. Operations are exposed to the model
through the ``knowledge`` tool, whose ``command`` field dispatches to the
manager:

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
-----------

When a base directory is set, the manager persists to
``<baseDir>/<project>/data.json`` after every change (``save``). The on-disk
schema is the simple ``{id, payload: {label, text}, vector}`` shape. On load,
the index is rebuilt from the persisted vectors, so the KB survives restarts
without re-embedding.

Seeding
-------

``EnsureSeeded`` populates the KB the first time it is used for a project, so
it starts with curated content instead of being empty. Global seeds under
``seedRoot/*.md`` are combined with project seeds under
``seedRoot/<project>/*.md``. Each file is stored with a label derived from its
path relative to ``seedRoot`` (prefixed with ``seed:``), so global and
per-project files with the same basename never collide. It is idempotent —
once the index has items, nothing is embedded again.

Wiring and build
----------------

The server builds the per-project ``Manager`` lazily on ``set_cwd`` from
``<baseDir>/<project>/data.json`` (clearing it when the KB is disabled), and
the ``knowledge`` tool calls are resolved **locally** inside ``runToolCycle``,
so they never wait on the client. A one-line note about the project's KB is
injected on the first turn when the KB is enabled.

The KB requires the ONNX build:

.. code-block:: sh

   make build-kb

This needs ``CGO_ENABLED=1`` and the libtokenizers ``CGO_LDFLAGS`` (set in the
Makefile), the assets in ``~/.cache/llm-bridge/`` (downloaded by
``scripts/fetch_kb.sh``) and the environment variable
``LLM_BRIDGE_ONNXRUNTIME_LIB="$HOME/.cache/llm-bridge/libonnxruntime.so"``.

Without those, the KB starts disabled with a warning and the bridge keeps
running (the ``knowledge`` tool is absent). The default ``make build`` (no
``knowledge_onnx`` tag) likewise produces a build without a working KB.

Repopulating from seeds
-----------------------

A standalone mode fully rebuilds a project's KB from a seed directory
(a **total** wipe + re-embed), so edits to the seed are reflected:

.. code-block:: sh

   ./build/llm-bridge -populate-project-kb <seedDir> -project <proj>

This uses the same ``seedRoot/*.md`` plus ``seedRoot/<project>/*.md`` layout as
``EnsureSeeded`` and requires the ONNX embedder. The seed stays decoupled from
the binary — it is only read at populate time.
