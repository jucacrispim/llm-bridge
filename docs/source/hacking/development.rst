Development
===========

Guidelines and references for working on the bridge code.

Directory layout
----------------

The bridge is organized into focused packages (see :doc:`architecture` for how
they fit together):

``cmd/bridge``
    The executable entry point. Parses the CLI flags, builds the provider
    registry, wires the knowledge base and logging, and calls
    ``server.RunWithKnowledgeBase``. Also implements the standalone
    ``-populate-project-kb`` mode.

``server``
    The core: the ``state`` struct, ``handleLine``, ``runToolCycle``,
    ``cancelTurn`` and the ``run`` entry point. Holds the embedded default
    system prompt (``system_prompt.md`` via ``//go:embed``).

``llm``
    The provider abstraction (``LLMProvider``, ``ChatRequest``,
    ``ChatResponse``, ``Message``, ``ToolCall``) and the **DeepSeek** and
    **Google/Gemini** implementations.

``protocol``
    Wire types and helpers: ``InboundCommand`` (client → bridge) and the
    outbound event constructors (``NewChunk``, ``NewToolCall``, ``NewTurnEnd``,
    ``NewHook``, …).

``tools``
    ``tools.All()`` returns the tool definitions exposed to the LLM: ``read``,
    ``write``, ``shell``, ``grep``, ``glob``, ``search_replace`` and
    ``knowledge``.

``hooks``
    Resolves and executes ``#``-prefixed hook scripts (see
    :doc:`hooks-internals`).

``knowledge``
    The per-project knowledge base: index, embedders, ``Manager`` and seed
    handling (see :doc:`knowledge-internals`).

``context``
    Loads the Markdown context files injected on the first turn (see
    :doc:`context-internals`).

``history``
    Helpers to manage the conversation history slice (append/sanitize,
    ephemeral stripping, collapse/prune).

``logger``
    The leveled logger used across the bridge (logs to a file, never to
    stdout).

Testing
-------

Run the test suite with:

.. code-block:: sh

   $ make test        # go build ./... && go test -v ./...

The server package has focused unit tests (``server/server_test.go``)
covering helpers that were historically untested:

- ``TestRecordFileChanged`` — path guard, append and dedup of
  ``turnFilesChanged``.
- ``TestParseHook`` — ``#ls -l`` → ``("ls", ["-l"])``, ``#algo`` →
  ``("algo", nil)``, ``#`` → ``("", nil)``.
- ``TestFilePathFromArgs`` — extracting the ``path`` from tool-call JSON
  arguments.

The hook branch in ``handleLine`` is exercised asynchronously:
``TestHandleLineHook`` / ``TestHandleLineHookMissing`` read the ``hook_action``
event off the writer (with a timeout) because hooks run in a goroutine and do
not return a synchronous response.

``files_changed`` and automated tests
-------------------------------------

When the tool loop writes files via the ``write`` / ``search_replace`` tools,
the bridge emits a ``files_changed`` event after ``turn_end`` listing the
modified paths (see :doc:`/protocol`). A client (e.g. Emacs) can hook into this
to trigger automated tests for the changed Go files — e.g. running
``go test ./<dir> -v -run '<Test funcs>'`` for the affected package. The
``custom-commands.el`` helper in this repo shows one approach.

.. note::

   ``files_changed`` is only emitted when the **bridge** writes files through
   its ``write``/``search_replace`` tools. Manual edits made outside the bridge
   (e.g. editing a file with a different editor) do **not** produce this event,
   so the automated test will not run in that case.
