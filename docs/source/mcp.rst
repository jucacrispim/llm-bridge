MCP servers
===========

Point the bridge at local commands that expose tools, and the model can call
them like the built-in ones. Each server is a binary you write or install. It
reads one JSON request from stdin and prints JSON values on stdout.

Configuration
-------------

The bridge reads ``~/.config/llm-bridge/mcp.json``. Use ``-mcp-config`` to name
another file, and ``-mcp=false`` to turn the feature off. A missing file means
no servers.

.. code-block:: json

   {
     "servers": {
       "tupi": {
         "command": "~/mysrc/tupi/bin/tupi-tools",
         "ro": ["~/mysrc/tupi"],
         "read_only": ["search"],
         "idle": "20s",
         "total": "2m"
       }
     }
   }

``command``
   The binary. An absolute path, or a name found in ``$PATH``. A ``~`` at the
   front expands to your home directory.

``args``
   Arguments passed to the binary.

``cwd``
   Working directory of the server process.

``ro`` and ``rw``
   Extra read-only and read-write paths. The base policy is the same one the
   shell tool gets, so the server already sees ``/usr``, ``/etc`` and the
   projects root you passed to ``-sandbox-rw``.

``writable_git``
   Defaults to ``false``, where the bridge mounts the ``.git`` of every
   repository under the read-write roots read-only, so no tool edits hooks or
   config. A server that runs ``git add`` or ``git commit`` needs it writable:
   staging writes ``.git/index`` and committing writes objects and refs. The
   cost is that this server can write any ``.git`` under those roots.

``network``
   Defaults to ``false``, where loopback is the only interface, like every
   other tool. Set it to ``true`` for a server that reaches other hosts.

``ports`` and ``any_port``
   Destinations allowed with ``network: true``. The default is port 443.

``read_only``
   Tools that run without confirmation, named as the server names them. Every
   other tool goes to the client as ``tool_confirm`` first.

``idle`` and ``total``
   Bounds for one call, as Go durations. The defaults are ``20s`` of silence
   and ``5m`` for the whole call.

The wire
--------

The bridge sends one request on stdin and reads values until the server closes
stdout or a limit hits. A ``tools/list`` request answers with the tools:

.. code-block:: console

   $ echo '{"method":"tools/list"}' | tupi-tools
   {"tools":[{"name":"search","description":"Search the tupi code","inputSchema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}]}

A ``tools/call`` request answers with the result, which may come in pieces:

.. code-block:: console

   $ echo '{"method":"tools/call","params":{"name":"search","arguments":{"q":"index"}}}' | tupi-tools
   {"text":"cmd/tupi/index.go:42: findIndex\n"}
   {"text":"cmd/tupi/index.go:88: indexConfig\n"}

Each value is a fragment. The bridge joins the fragments and sends each one to
your client as a ``tool_output`` event while the call runs. The history gets the
joined text when the call ends.

The MCP shape works too. A value may carry ``content`` with ``type: text``
parts instead of ``text``, and the two may appear in the same stream. Set
``"isError": true`` on a value to mark the result as an error.

Tool names
----------

The model sees ``mcp__<server>__<tool>``, so two servers may both serve
``read``. Characters outside ``A-Za-z0-9_-`` become ``_``, and a name longer
than 64 characters is truncated with a short hash.

Limits and cancellation
-----------------------

A call stops when the server is silent for ``idle``, when ``total`` passes, or
when your client cancels the turn. The process group is killed. Output past
1 MiB stops the call too.

A call stopped by a limit is not an error. The result carries what arrived and a
line saying why it stopped:

.. code-block:: text

   cmd/tupi/index.go:42: findIndex
   [mcp: stopped after going idle]

The server runs in its own process group, in private namespaces, under the
policy above. Cancelling a turn kills it and whatever it spawned, the same way
a cancelled shell command dies.

Startup
-------

The bridge lists the tools of every configured server at startup, one process
per server, in parallel. A server that fails to answer is left out with a
warning in the log file, and the bridge starts anyway. To see the warnings, run
the bridge with ``-logfile``.

A tool the server announces without an ``inputSchema`` gets
``{"type": "object"}``, so the providers accept the call.
