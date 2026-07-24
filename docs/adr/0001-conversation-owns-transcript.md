# ADR 0001: Conversation owns session and transcript models

Status: accepted

## Context

Session metadata and durable messages previously lived in the context package.
This forced the session service to depend on context compaction even though
session lifecycle is a more fundamental capability.

## Decision

`internal/conversation` owns Session, Message, Turn and durable transcript
types. `internal/context` consumes those types to build a bounded model view.
The file-backed implementation lives in `internal/storage/fileconversation`
and implements both consumer-defined store interfaces.

## Consequences

- Session behavior no longer depends on the compaction subsystem.
- Alternative storage implementations can be added without changing the agent
  or conversation service.
- Context currently exposes compatibility aliases for stored transcript types;
  these can be removed once downstream code imports conversation directly.
- Moving the store is structural only: existing JSON and JSONL formats remain
  compatible.
