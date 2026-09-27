Knowledge Base
==============

The bridge ships a small, local, per-project knowledge base (KB) that the model
can use during a conversation through the ``knowledge`` tool. It stores short
notes about what has been learned and lets the model retrieve them later with a
similarity search, scoped to the current working directory.

Using the ``knowledge`` tool
----------------------------

The model drives the KB through the ``knowledge`` tool, whose ``command`` field
selects the operation:

- ``show`` — lists the stored labels (a human-readable summary).
- ``search <query>`` — returns the most similar notes with their scores.
- ``add <label> <text>`` — stores ``text`` under ``label``. It is an **upsert
  by label**: re-adding an existing label replaces its text instead of
  inserting a duplicate. The label is the stable identifier used both to store
  a note and to correct one written earlier.
- ``delete <label>`` — removes the item with that label.
- ``reset`` — clears the KB and rebuilds it from the seed.

Enabling the KB
---------------

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

Seeding
-------

The first time a project uses the KB it is populated from curated seed files,
so it starts with content instead of being empty. Global seeds under
``seedRoot/*.md`` are combined with project seeds under
``seedRoot/<project>/*.md``. It is idempotent — once the KB has items, nothing
is re-embedded.

Repopulating from seeds
-----------------------

A standalone mode fully rebuilds a project's KB from a seed directory
(a **total** wipe + re-embed), so edits to the seed are reflected:

.. code-block:: sh

   ./build/llm-bridge -populate-project-kb <seedDir> -project <proj>

This uses the same ``seedRoot/*.md`` plus ``seedRoot/<project>/*.md`` layout as
seeding and requires the ONNX embedder. The seed stays decoupled from the
binary — it is only read at populate time.

Implementation details (the ``knowledge`` package, index, embedders, the
``Manager`` and persistence) live in :doc:`/hacking/knowledge-internals`.
