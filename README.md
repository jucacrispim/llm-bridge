# llm-bridge

**llm-bridge** is a bridge written in Go that mediates between a client (such as
Emacs) and several LLM providers (**DeepSeek** and **Google/Gemini**), exposing
them all through a single, unified JSON-lines protocol over stdio.

It reads commands from the client on stdin, forwards the conversation to the
active provider, and streams the response back on stdout — translating the
request/response format, streaming style, authentication and thinking mode for
each provider. Beyond plain chat, the bridge relays client-side tool calls (the
model calls the tools, the client runs them), carries image attachments, injects
project context, offers an optional per-project knowledge base, and runs hooks.

## Documentation

Full documentation, including the protocol, features and internals:

**https://docs.poraodojuca.dev/llm-bridge/index.html**

## Build & run

```sh
make build        # build the bridge (no knowledge base)
make build-kb     # build with the ONNX knowledge base support
./build/llm-bridge --help
```

See the documentation above for the protocol, the CLI flags and the client side.
