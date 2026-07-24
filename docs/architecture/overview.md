# FFCode architecture

FFCode is a modular monolith. Packages are grouped by responsibility rather
than by transport or implementation detail.

## Dependency direction

```text
cmd/ffcode -> app -> ui/terminal
                  -> agent -> context -> conversation
                           -> llm
                           -> tool
                  -> storage/fileconversation
```

- `cmd/ffcode` only enters the application and returns its exit code.
- `app` is the composition root. Concrete providers, stores and tools are
  selected only here.
- `conversation` owns sessions, messages, turns and transcript models.
- `agent` owns the model/tool execution loop and publishes UI-neutral events.
- `context` builds the bounded view sent to the model; it does not own session
  lifecycle data.
- `tool` owns registry and authorized execution. Built-in implementations live
  in `tool/builtin`.
- `storage/fileconversation` implements conversation and context persistence
  without changing the existing on-disk format.
- `ui/terminal` reads terminal input and renders agent events. It does not
  construct infrastructure dependencies.

## Rules

1. Core packages must not import `app`, `ui` or `storage`.
2. Infrastructure implements interfaces defined by the package that consumes
   the capability.
3. Configuration and default implementation selection belong to `app`.
4. Context compaction may change a model view but must never rewrite the source
   transcript.
5. New tools implement `tool.Tool`; registration and policy defaults belong to
   the composition root.
