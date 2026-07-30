# Terminal Input And Loop Timing Design

## Goal

Improve the interactive terminal UI in two focused ways:

- Allow users to compose multiline messages. `Ctrl+Enter` inserts a newline at the cursor, while an unmodified `Enter` submits the complete message.
- Show the elapsed time of the current Agent loop in real time and retain its final duration when the loop ends.

## Scope

The change is limited to `internal/ui/terminal`. Multiline editing applies to interactive TTY input; piped input keeps its existing line-oriented behavior. Loop timing applies to the interactive terminal renderer and does not change the JSONL protocol.

## Input Component

Replace the single-line Bubbles `textinput.Model` in `promptModel` with `textarea.Model`. The textarea starts at one row and grows with its rendered content, including soft-wrapped lines, up to eight rows. Content beyond eight rows scrolls inside the input viewport.

Keep the existing visual treatment: prompt and text remain white, the cursor remains cyan, and the real terminal cursor remains enabled so system input methods can anchor their candidate window correctly.

## Keyboard Behavior

- `Enter` submits the complete textarea value and quits the prompt program.
- `Ctrl+Enter` is passed to the textarea as its newline action and never submits.
- `Ctrl+C` aborts the prompt.
- When slash-command hints are visible, `Enter` retains its current completion/confirmation behavior.
- `Up` and `Down` move within multiline input when the cursor can move in that direction. History navigation is used only from the first visual line with `Up`, or the last visual line with `Down`.
- `Tab`, `Esc`, and command-hint navigation keep their existing behavior.

Terminal key protocols are not uniform. The behavior is guaranteed when Bubble Tea receives `Ctrl+Enter` as a distinct modified key. Terminals that encode it identically to `Enter` cannot be distinguished by the application.

## State And Rendering

After every value or width change, synchronize the textarea height to its rendered line count, clamped to the range 1 through 8. Window resize continues to determine the available input width.

Submitted rendering includes the prompt followed by the complete multiline value and a trailing newline. Command hint rendering remains below the input area. Existing history entries may contain newlines and are restored without flattening them.

The current custom horizontal viewport and cursor-offset bookkeeping is removed. The textarea owns multidimensional cursor positioning, wrapping, and scrolling.

## Loop Timing

Start the elapsed timer immediately before calling `Runner.RunContext` and stop it when the Agent event stream closes. This measures one user-triggered Agent loop and excludes time spent composing the message.

Extend the existing transient status line rather than adding another terminal row. Refresh it once per second and append whole-second elapsed time to the current state, for example `正在思考 · 12s` or `正在调用 read_file · 18s`. Status changes preserve the timer, and timer ticks preserve the current status text. The elapsed display remains available during periods without Agent events.

When the loop ends, render the final duration to 100 millisecond precision next to the existing token usage summary. Completed, interrupted, and failed loops all report their actual elapsed time. The ticker is stopped on every exit path and does not leave a background goroutine running.

## Tests

Add focused terminal input tests that verify:

- `Ctrl+Enter` inserts a newline without submitting.
- `Enter` submits the complete multiline value.
- Input height grows with explicit and soft-wrapped lines and stops at eight rows.
- Multiline cursor movement takes precedence over history navigation, while boundary navigation still recalls history.
- Slash-command completion still works with the textarea.
- The transient status line refreshes elapsed time without an Agent event.
- Status changes retain the current elapsed time.
- Completed, interrupted, and failed loops render a final duration.
- The timing ticker stops when event consumption ends.

Run `gofmt` on modified Go files, then run the terminal package tests and `go test ./...`.
