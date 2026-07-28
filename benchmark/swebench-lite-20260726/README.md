# SWE-bench Lite Runner

`run.py` can run either FFCode or Codex against the tasks in `tasks.jsonl`. Each
case gets an isolated Git repository. Agent logs, patches, restartable state, and
SWE-bench prediction JSONL are written below the selected `--root` directory.

## Codex smoke run

Use the Codex CLI bundled with the ChatGPT desktop application. On this machine,
the `codex` found first in `PATH` is a separate wrapper which performs its own
network version check, so pass the desktop CLI path explicitly.

Create a one-case input and run it:

```bash
sed -n '1p' tasks.jsonl > /tmp/swebench-lite-1.jsonl
python3 -u run.py \
  --agent codex \
  --binary /Applications/ChatGPT.app/Contents/Resources/codex \
  --model gpt-5.6-sol \
  --model-name Codex-gpt-5.6-sol \
  --tasks /tmp/swebench-lite-1.jsonl \
  --root /tmp/codex-swe-lite-1 \
  --workers 1 \
  --timeout 1200
```

For a ten-case smoke run, change the first command to:

```bash
sed -n '1,10p' tasks.jsonl > /tmp/swebench-lite-10.jsonl
```

Then use that file as `--tasks`, choose a new `--root`, and start with two
workers. A rerun with the same root resumes cases which do not already have a
terminal result.

Codex runs non-interactively with JSONL output, ephemeral sessions, and the
`workspace-write` sandbox. It uses the normal Codex authentication and provider
configuration. The runner disables package downloads in the child environment.

## Live official evaluation

`watch_evaluator.py` can evaluate patches as the agent finishes. It requires a
working SWE-bench harness checkout, Python environment, and Docker runtime:

```bash
python3 -u watch_evaluator.py \
  --agent-state /tmp/codex-swe-lite-10/agent-results.jsonl \
  --root /tmp/codex-swe-lite-10/evaluator \
  --harness /path/to/SWE-bench \
  --harness-python /path/to/SWE-bench/.venv/bin/python \
  --report-dir ./live-reports-codex \
  --model-name Codex-gpt-5.6-sol \
  --run-prefix codex-lite-smoke \
  --expected 10 \
  --workers 2
```

The runner outputs `/tmp/codex-swe-lite-10/predictions.jsonl` for standalone
official evaluation as well.

## Tests

```bash
python3 -m unittest -v test_run.py test_watch_evaluator.py
```
