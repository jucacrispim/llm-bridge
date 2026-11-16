Development
===========

Guidelines and references for working on the bridge code. The packages and what
each one owns are in :doc:`architecture`, and a new package has to land there
too.

Testing
-------

Run the test suite with:

.. code-block:: sh

   $ make test        # go build ./... && go test -v ./...

The server package has focused unit tests (``server/server_test.go``):

- ``TestRecordFileChanged`` - path guard, append and dedup of
  ``turnFilesChanged``.
- ``TestParseHook`` - ``#ls -l`` → ``("ls", ["-l"])``, ``#algo`` →
  ``("algo", nil)``, ``#`` → ``("", nil)``.
- ``TestFilePathFromArgs`` - extracting the ``path`` from tool-call JSON
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
to trigger automated tests for the changed Go files, e.g.
``go test ./<dir> -v -run '<Test funcs>'`` for the affected package. The
``custom-commands.el`` helper in this repo shows one approach.

.. note::

   ``files_changed`` is only emitted when the **bridge** writes files through
   its ``write``/``search_replace`` tools. Manual edits made outside the bridge
   (e.g. editing a file with a different editor) do **not** produce this event,
   so the automated test will not run in that case.
