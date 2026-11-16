Knowledge Base
==============

The bridge ships a small local knowledge base (KB) that the model uses through
the ``knowledge`` tool. It stores short notes and retrieves them later by
similarity search.

There are two bases, and ``-kb-scope`` picks which one(s) you get:

- ``global`` (**the default**): one shared KB, independent of the cwd. A note
  collected in one project is there in every other one.
- ``project``: one KB per project, keyed by the last element of the cwd. Notes
  stay in the project they were learned in.
- ``both``: the shared KB plus the project's, at the same time.

A prompt changes this for the conversation with its ``kb_scope`` field
(``"global"``, ``"project"`` or ``"both"``). Like the ``model`` and ``provider``
overrides, it persists across the following prompts.

Using the ``knowledge`` tool
----------------------------

The system prompt and the tool description (both static, so they sit early and
stay cache-friendly) tell the model to consult the KB before reasoning from
scratch about how the project works, its architecture or a past decision. They
also tell it to record durable findings with ``add``. The ``command`` field
selects the operation:

- ``show``: list the stored labels.
- ``search <query>``: the most similar notes, with their scores.
- ``add <label> <text>``: store ``text`` under ``label``. It is an **upsert by
  label**, so re-adding a label replaces its text instead of inserting a
  duplicate. That is how the model corrects a note it wrote earlier.
- ``delete <label>``: remove the item with that label.
- ``reset``: clear the KB and rebuild it from the seed.

``show`` and ``search`` cover every active KB. With two active, the results
merge by score and each label carries the scope it came from (``global: …`` or
``project: …``). ``add``, ``delete`` and ``reset`` take an optional ``scope``
(``"global"`` or ``"project"``) and default to the global KB, so a note is shared
unless the model asks for the project's.

Enabling the KB
---------------

The KB needs the ONNX build:

.. code-block:: sh

   $ make build-kb

That needs ``CGO_ENABLED=1``, the libtokenizers ``CGO_LDFLAGS`` (set in the
``Makefile``), the assets in ``~/.cache/llm-bridge/`` (``scripts/fetch_kb.sh``
downloads them) and one variable:

.. code-block:: sh

   $ export LLM_BRIDGE_ONNXRUNTIME_LIB="$HOME/.cache/llm-bridge/libonnxruntime.so"

Without them the KB starts disabled with a warning, and you get a working bridge
without the ``knowledge`` tool. A plain ``make build`` has no ``knowledge_onnx``
tag and gives you no working KB either.

The base directory defaults to ``~/.local/share/llm-bridge/knowledge_bases``, and
``-knowledge-base`` or ``$KNOWLEDGE_BASE`` changes it. The shared KB is
``knowledge_bases/_global/data.json`` and a project KB is
``knowledge_bases/<project>/data.json``, so switching scope discards nothing.

Seeding
-------

The first time a KB is used it can be populated from curated seed files, so it
starts with content instead of empty. The layout is:

.. code-block:: text

   <seedRoot>/*.md                global seeds
   <seedRoot>/<project>/*.md      project seeds

The shared KB takes the global seeds only, because there is no ``_global`` seed
subdirectory. Seeding is idempotent: a KB that already has items is left alone.

Any directory works, and ``~/.local/share/llm-bridge/seeds`` is the conventional
one, next to the bases. Only the layout above matters, and the seed is read from
disk at populate time, never compiled into the binary.

Repopulating from seeds
-----------------------

A standalone mode rebuilds a project's KB from the seed and exits. It wipes the
existing ``data.json`` and re-embeds everything, so an edit to a seed file shows
up:

.. code-block:: sh

   $ ./build/llm-bridge -populate-project-kb ~/.local/share/llm-bridge/seeds -project <project>

Use ``-project _global`` for the shared KB, which rebuilds from the global seeds
only. Like the seeding above, this needs the ONNX embedder.

The ``knowledge`` package, the index, the embedders, the ``Manager`` and the
persistence are in :doc:`/hacking/knowledge-internals`.
