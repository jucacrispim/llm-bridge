llm-bridge
==========

**llm-bridge** is a bridge that mediates between several LLM providers and
exposes them all through a single, unified API. No matter how different the
providers are — request/response format, streaming style, authentication,
thinking mode — a client talks to the bridge once, in one common protocol, and
lets the bridge translate that into whichever provider is active.

.. toctree::
   :maxdepth: 2

   overview
   installation

.. toctree::
   :maxdepth: 2

   usage
   protocol
   tools

.. toctree::
   :maxdepth: 2

   context

.. toctree::
   :maxdepth: 2

   knowledge
   hooks

.. toctree::
   :maxdepth: 2

   hacking/index

Indices and tables
------------------

* :ref:`genindex`
* :ref:`modindex`
* :ref:`search`
