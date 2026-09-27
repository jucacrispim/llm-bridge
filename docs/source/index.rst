llm-bridge
==========

**llm-bridge** is a bridge that mediates between several LLM providers and
exposes them all through a single, unified API. No matter how different the
providers are — request/response format, streaming style, authentication,
thinking mode — a client talks to the bridge once, in one common protocol, and
lets the bridge translate that into whichever provider is active.

.. toctree::
   :maxdepth: 2
   :caption: Installation

   installation

.. toctree::
   :maxdepth: 2
   :caption: Usage

   usage
   protocol

.. toctree::
   :maxdepth: 2
   :caption: Reference

   providers
   hooks
   knowledge
   context

.. toctree::
   :maxdepth: 2
   :caption: Internals

   overview
   architecture
   development

Indices and tables
------------------

* :ref:`genindex`
* :ref:`modindex`
* :ref:`search`
