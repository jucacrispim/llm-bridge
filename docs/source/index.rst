llm-bridge
==========

Use **llm-bridge** to connect your editor or another client to Anthropic,
DeepSeek, Google Gemini, or OpenAI. Send prompts and receive streamed replies
over a JSON Lines protocol on stdin and stdout. To switch providers, keep the
same client and change the provider in your request.

Features
--------

- **Streaming.** Receive replies as they arrive, with separate reasoning events
  when the provider exposes them.
- **Image inputs.** Attach screenshots, diagrams, or other images to prompts
  when the selected model supports them.
- **Model controls.** Choose a model and adjust its reasoning settings without
  restarting the bridge.
- **Sandboxed tools.** Let the model search files, edit code, and run shell
  commands. The bridge uses ``caged`` to limit what tools can read or write.
  Shell commands only have loopback network access. The web fetch tool runs
  in a separate sandbox without access to your project files. Review file
  edits, shell commands, and web requests in your client before they run.
- **Project context.** Load Markdown instructions from your home and project
  directories.
- **Local knowledge base.** Enable the optional knowledge base to save and
  search notes across sessions. Keep notes shared or scoped to a project.
- **Hooks.** Run a local script with a ``#`` command instead of sending a prompt
  to the model.
- **Usage and cost.** Track tokens and timing per turn. See estimated costs
  per turn and session when model pricing is available.

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
