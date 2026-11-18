Overview
========

**llm-bridge** is a bridge written in Go between a client (an Emacs buffer, for
example) and LLM providers. It reads JSON-lines commands on **stdin**, forwards
the conversation to the active provider, and writes the provider's streamed
response back as JSON-lines on **stdout**. Logs go to a file (``-logfile``), so
stdout stays clean for your client.

Features
--------

The bridge sits between your client and the provider and gives you one stable
surface. A plain provider SDK gives you one per provider:

- **Unified API across providers.** ``prompt``, ``tool_confirm``, ``cancel`` and
  the rest mean the same thing whether the active provider is **DeepSeek**,
  **Google/Gemini**, **Anthropic/Claude** or **OpenAI**. The bridge translates
  the request format, the streaming style, the authentication and the thinking
  mode for each one.
- **Tools executed by the bridge.** The models already know how to call tools.
  The bridge advertises the tool schemas and runs the tools itself: ``read``,
  ``write``, ``search_replace``, ``grep``, ``glob``, ``shell`` and ``curl``.
  Read-only tools run without confirmation. A mutating one (``shell``,
  ``write``, ``search_replace``, ``curl``) reaches your client as a
  ``tool_confirm`` event first.
- **Images.** A prompt carries an image by ``path`` (the bridge reads it), by
  ``url`` (passed through to the provider) or by ``data`` (inline base64, what a
  clipboard paste sends). The bridge converts it to the provider's format: an
  ``image_url`` content part for DeepSeek, an inline or ``fileData`` part for
  Gemini, an ``image`` block with a base64 or URL source for Claude, and an
  ``input_image`` part for OpenAI.
- **Provider switching per request.** **DeepSeek**, **Google/Gemini**,
  **Anthropic/Claude** and **OpenAI** are registered up front. A prompt can
  switch the provider, the model, the thinking mode, the reasoning effort or the
  system prompt, and the override persists across the following prompts.
- **Hooks.** A message starting with ``#`` runs a local script instead of the
  LLM, which is handy for project commands. See :doc:`hooks`.
- **Context and knowledge base.** Markdown context is injected into the
  conversation, general files first and then the project's. An optional
  knowledge base (shared across projects by default, or one per project) with an
  ONNX embedder answers through the ``knowledge`` tool.
- **History management.** ``-prune`` and ``-aggressive-prune`` collapse finished
  turns, which saves tokens and keeps the prefix cacheable.

Basic conversation flow
-----------------------

#. You start the bridge and send a ``prompt`` command on stdin.
#. The bridge appends the message to the history. On the first turn it also
   injects the context files and, with the KB on, a one-line note about it.
#. It calls the active provider: the default from ``--provider``, switchable
   with the ``provider`` override.
#. The provider's stream comes back as events: ``chunk`` for content,
   ``thinking`` for the chain-of-thought, ``tool_call`` for a read-only tool the
   bridge already ran, ``tool_confirm`` for a mutating one waiting on you,
   ``usage_delta``, and ``turn_end`` at the end.
#. When the model asks for a tool, the bridge runs the read-only ones at once.
   For a mutating one it emits ``tool_confirm`` and waits for your approval. The
   tool loop (``runToolCycle``) keeps the calls and the results in the history
   and repeats until the model produces a final answer.
#. When the model finishes, the turn ends. If ``write`` or ``search_replace``
   touched files, their paths go out in a ``files_changed`` event, so a client
   can refresh a buffer or run tests.

The protocol is line-based, so the loop stays simple: one command per line in,
one event per line out. Long work runs off the main loop, and that is what keeps
the bridge answering ``cancel``, ``tool_confirm`` and new prompts while a chat
streams or a hook script runs.

Where it runs
-------------

The bridge is a single binary on your host, launched by the client as a
subprocess. It opens no network port: the client spawns ``./build/llm-bridge
[flags]`` and talks over the stdin and stdout pipes. The model's tools therefore
work on ``<cwd>`` directly.

The protocol is in :doc:`protocol`, tool calls in :doc:`tools`, the providers in
:doc:`/hacking/providers`, hooks in :doc:`hooks`, the knowledge base in
:doc:`knowledge`, context loading in :doc:`context`, and the flags in
:doc:`usage`.
