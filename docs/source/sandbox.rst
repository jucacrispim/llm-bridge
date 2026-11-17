The sandbox binding
===================

The bridge runs every external process through `caged
<https://docs.poraodojuca.dev/caged>`_. The policy flags and the default
policies are in :doc:`usage`. This document covers the Go side in
``sandbox/``: how a policy becomes caged's argv, and what the bridge can learn
from one caged run.

The report descriptor
---------------------

The exit status does not tell you whether the sandbox applied. caged calls
``execvp``, so caged and the command share one status space. ``ls`` on a
missing file exits 2, exactly like a caged that rejected the policy, and any
program exiting 125 looks like a failed setup.

The bridge reads a descriptor instead. caged opens the one named by
``--report-fd`` (``3``, the first free descriptor above stderr) and writes its
own diagnostics there. It writes nothing when the setup succeeded, so an empty
descriptor means the sandbox applied and whatever happened after that was the
command's doing.

``Sandbox.Run`` returns a ``*SetupError`` when the descriptor carries anything.
Surface that as an error, never as the tool result: the command did not run
confined, or did not run at all. The constants ``ExitUsage`` (2) and
``ExitSetupFailed`` (125) only shape the error message.

.. note::

   ``requireReportFD`` checks ``--help`` at startup. An old caged without
   ``--report-fd`` is a startup error, never a downgrade to guessing.

Two caged limitations
---------------------

**Descriptors.** caged calls ``close_range(3, ~0)`` before ``exec``, so every
inherited descriptor above stderr is gone in the command. Only pipes survive,
and only the report descriptor is spared. ``Sandbox.Run`` owns
``cmd.ExtraFiles`` for the report pipe. Callers have no room to add a
descriptor of their own.

**Environment.** ``cmd.Env`` is the environment of the caged process, not of
the command. caged filters it to a fixed base set (``PATH``, ``HOME``,
``LANG``, ``LANGUAGE`` and the ``LC_*`` variables) and adds the policy's
``--env`` entries. With ``KeepEnv`` the whole environment passes through. The
bridge never sets it.

Process group and cancellation
------------------------------

The command runs in its own process group. Cancelling the context kills the
whole group, because a ``bash -c`` command may have spawned children of its
own. Killing only the leader would leave them alive.

``WaitDelay`` bounds the other end. A grandchild that outlives the kill can
hold the output pipe open, and without a deadline the bridge would block on
it.
``Sandbox.Command`` sets five seconds.

Why the binding lives here
--------------------------

``sandbox`` builds argv and reads one descriptor. caged is a separate binary
with its own CLI contract and its own release cycle. Keeping the binding in
this repository means a caged flag rename is one commit, not a version matrix.

Policy fields and flags
-----------------------

``Policy.Args`` emits one flag for each field that is set:

.. list-table::
   :header-rows: 1
   :widths: 30 35 35

   * - Field
     - Flag
     - Note
   * - ``ReadOnly``
     - ``--ro`` (one per path)
     - The path must exist when caged runs.
   * - ``ReadWrite``
     - ``--rw`` (one per path)
     - Same existence rule.
   * - ``PrivateTmp``
     - ``--private-tmp``
     - A fresh tmpfs over ``/tmp``.
   * - ``PrivateTmpPath``
     - ``--private-tmp PATH``
     - A bind mount instead of a tmpfs. Implies the flag.
   * - ``PrivateShm``
     - ``--private-shm``
     - A fresh tmpfs over ``/dev/shm``.
   * - ``LoopbackOnly``
     - ``--loopback-only``
     - Does nothing alone. See below.
   * - ``AllowNetworkSockets``
     - ``--allow-network-sockets``
     - ``AF_INET``, ``AF_INET6`` and ``AF_NETLINK``.
   * - ``AllowUnixSockets``
     - ``--allow-unix-sockets``
     - ``AF_UNIX``.
   * - ``ConnectTCP``
     - ``--allow-connect-tcp``
     - Conflicts with ``ConnectPorts``.
   * - ``ConnectPorts``
     - ``--allow-connect-port``
     - Port 0 means the ephemeral range.
   * - ``BindTCP``
     - ``--allow-bind-tcp``
     - Conflicts with ``BindPorts``.
   * - ``BindPorts``
     - ``--allow-bind-port``
     - One flag per port.
   * - ``KeepEnv``
     - ``--keep-env``
     - The bridge never sets it.
   * - ``Env``
     - ``--env NAME=VALUE``
     - Added on top of the base set.
   * - ``MaxProcs``
     - ``--max-procs``
     - Zero omits the flag. caged rejects 0.
   * - ``RequireABI``
     - ``--require-abi``
     - Zero omits the flag. caged rejects 0.

``LoopbackOnly`` needs a paragraph of its own. It emits ``--loopback-only`` and
changes nothing by itself, because seccomp still denies ``socket(AF_INET)``.
Set it together with ``AllowNetworkSockets`` and the connect and bind flags:

.. code-block:: go

   p.LoopbackOnly = true
   p.AllowNetworkSockets = true
   p.ConnectTCP = true
   p.BindTCP = true

A destination outside ``127.0.0.0/8`` does not exist in that network
namespace, so the connection fails with ``ENETUNREACH``. The bridge builds this
group for every tool call: the namespace is the bound, and the released socket
families and ports are what let a tool reach its own loopback at all.

The private volumes are mounts, and caged applies them before it creates the
Landlock rules. A ``ReadWrite`` entry of ``/tmp`` therefore names the private
``/tmp``, not the host's. Both mounts need the user namespace, so the bridge
probes them at startup together with the network namespace (see
``probeNamespaces``).

Run and RunIO
-------------

``Run`` returns the combined stdout and stderr. A shell command wants that: the
model should see both streams in the order the command wrote them.

``RunIO`` keeps stdout and stderr apart and sends a stdin payload. It serves
``llm-tools``, which answers one JSON request with one JSON response. A single
warning on stderr would corrupt a combined result.

Both share the setup-failure contract. A ``*SetupError`` means the command did
not run under the policy, and stdout is not a result.
