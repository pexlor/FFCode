#!/usr/bin/env python3
"""Run a coding agent on SWE-bench Lite instances and emit prediction JSONL."""

import argparse
import concurrent.futures
import json
import os
import re
import select
import shutil
import signal
import subprocess
import sys
import tarfile
import threading
import time
import urllib.request
from pathlib import Path


PROMPT = (
    "Solve this SWE-bench issue. Inspect the repository, implement the fix, "
    "and run the relevant tests. Make actual code changes in the workspace; "
    "do not only explain. Do not install packages or change the environment; "
    "use the dependencies already available and continue if a test cannot run. "
)


class ProtocolEventParser:
    """Incrementally extracts terminal events from MyCode protocol v1 JSONL."""

    def __init__(self):
        self.buffer = ""

    def feed(self, text):
        self.buffer += text
        terminal_events = []
        while "\n" in self.buffer:
            line, self.buffer = self.buffer.split("\n", 1)
            try:
                event = json.loads(line)
            except (json.JSONDecodeError, TypeError):
                continue
            if event.get("version") != 1 or event.get("type") != "turn_finished":
                continue
            data = event.get("data")
            if not isinstance(data, dict):
                continue
            status = data.get("status")
            stop_reason = data.get("stop_reason")
            if status not in ("completed", "incomplete", "failed", "cancelled"):
                continue
            if not isinstance(stop_reason, str):
                continue
            terminal_events.append({"status": status, "stop_reason": stop_reason})
        return terminal_events


def run_cmd(args, cwd=None, timeout=180):
    return subprocess.run(args, cwd=cwd, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=timeout, check=True).stdout


def load_state(path):
    if not path.exists():
        return {}
    return {item["instance_id"]: item for item in
            (json.loads(line) for line in path.read_text().splitlines() if line.strip())}


def save_state(path, state):
    tmp = path.with_suffix(".tmp")
    tmp.write_text("".join(json.dumps(state[key], ensure_ascii=False) + "\n"
                                  for key in sorted(state)))
    tmp.replace(path)


def build_agent_command(args, worktree):
    if args.agent == "codex":
        command = [
            str(args.binary),
            "exec",
            "--json",
            "--ephemeral",
            "--sandbox",
            "workspace-write",
        ]
        if args.model:
            command.extend(["--model", args.model])
        command.extend(["--cd", str(worktree), "-"])
        return command
    return [
        str(args.binary),
        "--cwd",
        str(worktree),
        "--output-format",
        "jsonl",
    ]


def classify_agent_status(agent, timed_out, returncode, turn_status):
    if timed_out:
        return "timeout"
    if agent == "codex":
        return "completed" if returncode == 0 else "agent_error"
    if returncode != 0 or not turn_status or turn_status == "failed":
        return "agent_error"
    return turn_status


def write_predictions(path, tasks, state, model_name):
    with path.open("w") as out:
        for task in tasks:
            item = state.get(task["instance_id"], {})
            patch = Path(item.get("patch_path", ""))
            out.write(json.dumps({
                "instance_id": task["instance_id"],
                "model_name_or_path": model_name,
                "model_patch": patch.read_text(errors="replace") if patch.exists() else "",
            }, ensure_ascii=False) + "\n")


def run_instance(task, args):
    instance_id = task["instance_id"]
    worktree = args.root / "worktrees" / instance_id
    archive = args.root / "archives" / f"{instance_id}.tar.gz"
    mirror = args.root / "mirrors" / f"{task['repo'].replace('/', '__')}.git"
    use_mirror = False
    log_path = args.root / "agent-logs" / f"{instance_id}.log"
    patch_path = args.root / "patches" / f"{instance_id}.patch"
    for path in (worktree, log_path.parent, patch_path.parent):
        path.parent.mkdir(parents=True, exist_ok=True) if path.suffix else path.mkdir(parents=True, exist_ok=True)
    started = time.time()
    result = {"instance_id": instance_id, "repo": task["repo"], "status": "running", "started_at": started}
    try:
        setup_lock = args.repo_locks[task["repo"]]
        with setup_lock:
            use_mirror = mirror.exists() and subprocess.run(
                ["git", "cat-file", "-e", f"{task['base_commit']}^{{commit}}"], cwd=mirror,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
            shutil.rmtree(worktree, ignore_errors=True)
            if use_mirror:
                worktree.mkdir(parents=True)
                archive_process = subprocess.Popen(
                    ["git", "archive", task["base_commit"]], cwd=mirror,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                extract_process = subprocess.run(
                    ["tar", "-xf", "-", "-C", str(worktree)], stdin=archive_process.stdout,
                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
                archive_process.stdout.close()
                archive_stderr = archive_process.communicate(timeout=600)[1]
                if archive_process.returncode != 0 or extract_process.returncode != 0:
                    raise RuntimeError(
                        f"local archive failed: git={archive_process.returncode} "
                        f"tar={extract_process.returncode} "
                        f"output={(archive_stderr + extract_process.stdout).decode(errors='replace')}")
            else:
                worktree.mkdir(parents=True)
                if not archive.exists() or archive.stat().st_size == 0:
                    archive.parent.mkdir(parents=True, exist_ok=True)
                    temporary = archive.with_suffix(f".tmp-{os.getpid()}")
                    if task["repo"] == "pylint-dev/pylint":
                        url = (f"https://ghproxy.net/https://github.com/{task['repo']}"
                               f"/archive/{task['base_commit']}.tar.gz")
                    elif task["repo"] == "sympy/sympy":
                        url = (f"https://gitee.com/mirrors/sympy/repository/archive/"
                               f"{task['base_commit']}.tar.gz")
                    else:
                        url = f"https://codeload.github.com/{task['repo']}/tar.gz/{task['base_commit']}"
                    last_error = None
                    for attempt in range(5):
                        try:
                            with urllib.request.urlopen(url, timeout=120) as response, temporary.open("wb") as output:
                                shutil.copyfileobj(response, output)
                            temporary.replace(archive)
                            break
                        except Exception as exc:
                            last_error = exc
                            temporary.unlink(missing_ok=True)
                            if attempt == 4:
                                raise last_error
                            time.sleep(2 ** attempt)
                run_cmd(["tar", "-xzf", str(archive), "--strip-components=1", "-C", str(worktree)], timeout=300)
            run_cmd(["git", "init"], cwd=worktree)
            run_cmd(["git", "config", "user.email", "swebench@localhost"], cwd=worktree)
            run_cmd(["git", "config", "user.name", "SWE-bench"], cwd=worktree)
            run_cmd(["git", "add", "-A"], cwd=worktree, timeout=300)
            run_cmd(["git", "commit", "-q", "-m", "base"], cwd=worktree, timeout=300)
        if args.agent == "ffcode":
            agent_dir = worktree / ".agent"
            agent_dir.mkdir()
            (agent_dir / "permission.yaml").write_text(
                "disabled: true\ndefault: allow\nworkspace:\n  root: .\n")
        prompt = PROMPT + " ".join(task["problem_statement"].split())
        env = os.environ.copy()
        env["GIT_TERMINAL_PROMPT"] = "0"
        env["PIP_NO_INDEX"] = "1"
        env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
        process = subprocess.Popen(build_agent_command(args, worktree), cwd=worktree,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, env=env)
        log = log_path.open("wb")
        process.stdin.write((prompt + "\n").encode())
        process.stdin.flush()
        if args.agent == "codex":
            process.stdin.close()
        deadline = time.time() + args.timeout
        output = bytearray()
        marker_buffer = ""
        protocol_parser = ProtocolEventParser()
        turn_status = ""
        stop_reason = ""
        while process.poll() is None and time.time() < deadline:
            ready, _, _ = select.select([process.stdout], [], [], 1)
            if not ready:
                continue
            chunk = os.read(process.stdout.fileno(), 8192)
            if not chunk:
                break
            log.write(chunk)
            log.flush()
            output.extend(chunk)
            text = chunk.decode("utf-8", "replace")
            marker_buffer = (marker_buffer + text)[-2000:]
            if "[y] Allow Once" in marker_buffer:
                process.stdin.write(b"y\n")
                process.stdin.flush()
                marker_buffer = marker_buffer.rsplit("[y] Allow Once", 1)[-1]
            terminal_events = protocol_parser.feed(text)
            if terminal_events:
                turn_status = terminal_events[-1]["status"]
                stop_reason = terminal_events[-1]["stop_reason"]
                process.stdin.close()
                break
        timed_out = time.time() >= deadline
        if process.poll() is None:
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        remainder = process.stdout.read()
        if remainder:
            log.write(remainder)
            output.extend(remainder)
        result["status"] = classify_agent_status(
            args.agent,
            timed_out,
            process.returncode,
            turn_status,
        )
        if stop_reason:
            result["stop_reason"] = stop_reason
        log.close()
        diff = subprocess.run(["git", "diff", "--binary", "--", "."], cwd=worktree,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False).stdout
        patch_path.write_bytes(diff)
        result["patch_path"] = str(patch_path)
        result["patch_bytes"] = len(diff)
        result["output_bytes"] = len(output)
    except Exception as exc:
        result["status"] = "runner_error"
        result["error"] = repr(exc)
    finally:
        result["duration_seconds"] = round(time.time() - started, 2)
        shutil.rmtree(worktree, ignore_errors=True)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--tasks", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--agent", choices=("ffcode", "codex"), default="ffcode")
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--model")
    parser.add_argument("--model-name")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--timeout", type=int, default=1200)
    args = parser.parse_args()
    # The agent process runs with each task worktree as its cwd, so a relative
    # binary path would otherwise be resolved against the wrong directory.
    args.binary = args.binary.resolve()
    if not args.model_name:
        if args.agent == "codex":
            args.model_name = f"Codex-{args.model or 'default'}"
        else:
            args.model_name = "MyCode-MiniMax-M3"
    args.root.mkdir(parents=True, exist_ok=True)
    for name in ("archives", "worktrees", "agent-logs", "patches"):
        (args.root / name).mkdir(exist_ok=True)
    tasks = [json.loads(line) for line in args.tasks.read_text().splitlines() if line.strip()]
    args.repo_locks = {task["repo"]: threading.Lock() for task in tasks}
    state_path = args.root / "agent-results.jsonl"
    state = load_state(state_path)
    pending = [task for task in tasks if state.get(task["instance_id"], {}).get("status") not in
               ("completed", "incomplete", "failed", "cancelled", "agent_error", "timeout")]
    print(f"tasks={len(tasks)} pending={len(pending)} workers={args.workers}", flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {pool.submit(run_instance, task, args): task for task in pending}
        for future in concurrent.futures.as_completed(futures):
            result = future.result()
            state[result["instance_id"]] = result
            save_state(state_path, state)
            print(json.dumps(result, ensure_ascii=False), flush=True)
    predictions = args.root / "predictions.jsonl"
    write_predictions(predictions, tasks, state, args.model_name)
    print(f"predictions={predictions}", flush=True)


if __name__ == "__main__":
    main()
