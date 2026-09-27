Installation
============

The bridge is a single Go binary. There are two build variants: a **plain**
build (no knowledge base) and a build **with the knowledge base** (ONNX). This
page covers the prerequisites and both builds.

Prerequisites
-------------

- **Go** and ``make``. See ``go.mod`` for the required toolchain version.
- A **C compiler** is only needed for the knowledge base build
  (``make build-kb`` sets ``CGO_ENABLED=1``).
- ``curl`` and ``tar`` if you use ``scripts/fetch_kb.sh`` to download the
  knowledge base assets.

API keys are read from the **environment at runtime**; they are not needed to
build:

- ``DEEPSEEK_API_KEY`` — for the DeepSeek provider.
- ``GOOGLE_API_KEY`` (or ``GEMINI_API_KEY``) — for the Google/Gemini provider.

Plain build (no knowledge base)
-------------------------------

.. code-block:: sh

   $ make build        # → build/llm-bridge

This does not use the ``knowledge_onnx`` build tag, so the knowledge base is
compiled out and the ``knowledge`` tool is not exposed. Everything else works
normally, and this is the build to use if you do not need the KB.

Build with the knowledge base (ONNX)
------------------------------------

The knowledge base embeds text locally with an ONNX model, so this build needs
CGO, the ``knowledge_onnx`` tag and the static ``libtokenizers`` library:

.. code-block:: sh

   $ make build-kb     # CGO_ENABLED=1 + -tags knowledge_onnx + CGO_LDFLAGS

It also needs the model/tokenizer assets and the ONNX runtime library, fetched
by the helper script:

.. code-block:: sh

   $ scripts/fetch_kb.sh

This downloads ``model.onnx``, ``tokenizer.json`` and ``libonnxruntime.so``
into ``~/.cache/llm-bridge/`` (plus the static ``libtokenizers.a`` into
``~/.cache/llm-bridge/libtokenizers/``). At runtime, point the bridge at the
downloaded ONNX runtime library:

.. code-block:: sh

   $ export LLM_BRIDGE_ONNXRUNTIME_LIB="$HOME/.cache/llm-bridge/libonnxruntime.so"

.. note::

   If the assets or ``LLM_BRIDGE_ONNXRUNTIME_LIB`` are missing, the knowledge
   base starts **disabled** with a warning and the bridge keeps running — the
   ``knowledge`` tool is simply absent. The KB never takes the bridge down.

See :doc:`knowledge` for how the knowledge base works and :doc:`usage` for the
KB-related flags and environment variables.

Next
----

See :doc:`usage` for how to run the bridge and the ways to call and use it.
