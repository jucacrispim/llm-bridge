llm-bridge
==========

**llm-bridge** is a bridge that mediates between several LLM providers and
exposes them through one API. Your client talks to the bridge once, in one
common protocol, and the bridge translates the request format, the streaming
style, the authentication and the thinking mode of whichever provider is
active.

.. toctree::
   :maxdepth: 2

   overview
   installation

.. toctree::
   :maxdepth: 2

   usage
   protocol
   tools
   sandbox

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

.. toctree::
   :maxdepth: 2

   changelog

Indices and tables
------------------

* :ref:`genindex`
* :ref:`modindex`
* :ref:`search`
