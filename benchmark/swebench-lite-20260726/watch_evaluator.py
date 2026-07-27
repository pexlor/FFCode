#!/usr/bin/env python3
"""Evaluate SWE-bench predictions as MyCode finishes each instance."""

import argparse
import json
import os
import subprocess
import time
from pathlib import Path


def load_jsonl(path):
    if not path.exists():
        return {}
    return {
        item["instance_id"]: item
        for item in (json.loads(line) for line in path.read_text().splitlines() if line.strip())
    }


def save_jsonl(path, state):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        "".join(json.dumps(state[key], ensure_ascii=False) + "\n" for key in sorted(state))
    )
    temporary.replace(path)


def classify_results(results, evaluated):
    pending = []
    skipped = []
    for instance_id in sorted(results):
        if instance_id in evaluated:
            continue
        item = results[instance_id]
        patch_path = Path(item.get("patch_path", ""))
        if item.get("patch_bytes", 0) > 0 and patch_path.is_file() and patch_path.stat().st_size > 0:
            pending.append(item)
        else:
            skipped.append(item)
    return pending, skipped


def write_prediction(path, result, model_name):
    patch = Path(result["patch_path"]).read_text(errors="replace")
    prediction = {
        "instance_id": result["instance_id"],
        "model_name_or_path": model_name,
        "model_patch": patch,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(prediction, ensure_ascii=False) + "\n")


def evaluate_instance(result, args):
    instance_id = result["instance_id"]
    prediction_path = args.root / "predictions" / f"{instance_id}.jsonl"
    log_path = args.root / "logs" / f"{instance_id}.log"
    write_prediction(prediction_path, result, args.model_name)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    run_id = f"{args.run_prefix}-{instance_id}"
    command = [
        str(args.harness_python), "-m", "swebench.harness.run_evaluation",
        "-d", args.dataset, "-s", args.split, "-p", str(prediction_path),
        "--max_workers", "1", "-t", str(args.timeout),
        "--cache_level", "instance", "--clean", "false",
        "-id", run_id, "--report_dir", str(args.report_dir),
    ]
    environment = os.environ.copy()
    environment["DOCKER_HOST"] = args.docker_host
    last_error = None
    for attempt in range(1, args.max_attempts + 1):
        started = time.time()
        with log_path.open("a") as log:
            log.write(f"\n=== attempt {attempt} started {started} ===\n")
            log.flush()
            completed = subprocess.run(
                command, cwd=args.harness, env=environment,
                stdout=log, stderr=subprocess.STDOUT, check=False,
            )
        report_path = (
            args.harness / "logs" / "run_evaluation" / run_id /
            args.model_name / instance_id / "report.json"
        )
        if completed.returncode == 0 and report_path.exists():
            report = json.loads(report_path.read_text())[instance_id]
            resolved = bool(report.get("resolved"))
            return {
                "instance_id": instance_id,
                "status": "resolved" if resolved else "unresolved",
                "resolved": resolved,
                "agent_status": result.get("status"),
                "patch_bytes": result.get("patch_bytes", 0),
                "attempts": attempt,
                "duration_seconds": round(time.time() - started, 2),
                "report_path": str(report_path),
                "log_path": str(log_path),
            }
        last_error = f"exit={completed.returncode}, report_exists={report_path.exists()}"
        if attempt < args.max_attempts:
            time.sleep(args.retry_delay)
    return {
        "instance_id": instance_id,
        "status": "evaluator_error",
        "resolved": False,
        "agent_status": result.get("status"),
        "patch_bytes": result.get("patch_bytes", 0),
        "attempts": args.max_attempts,
        "error": last_error,
        "log_path": str(log_path),
    }


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent-state", type=Path, default=Path("/tmp/mycode-swe-full/agent-results.jsonl"))
    parser.add_argument("--root", type=Path, default=Path("/tmp/mycode-swe-full/evaluator-watch"))
    parser.add_argument("--harness", type=Path, default=Path("/tmp/swebench-harness.ImQnZk"))
    parser.add_argument("--harness-python", type=Path, default=Path("/tmp/swebench-harness.ImQnZk/.venv/bin/python"))
    parser.add_argument("--report-dir", type=Path, default=Path("benchmark/swebench-lite-20260726/live-reports"))
    parser.add_argument("--dataset", default="SWE-bench/SWE-bench_Lite")
    parser.add_argument("--split", default="test")
    parser.add_argument("--model-name", default="MyCode-MiniMax-M3")
    parser.add_argument("--run-prefix", default="mycode-live-20260727")
    parser.add_argument("--docker-host", default="unix:///Users/fengrui03/.docker/run/docker.sock")
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--poll-seconds", type=int, default=5)
    parser.add_argument("--max-attempts", type=int, default=3)
    parser.add_argument("--retry-delay", type=int, default=30)
    parser.add_argument("--expected", type=int, default=300)
    return parser.parse_args()


def main():
    args = parse_args()
    args.root.mkdir(parents=True, exist_ok=True)
    args.report_dir = args.report_dir.resolve()
    state_path = args.root / "evaluation-results.jsonl"
    evaluated = load_jsonl(state_path)
    print(f"watching={args.agent_state} evaluated={len(evaluated)} expected={args.expected}", flush=True)
    while len(evaluated) < args.expected:
        results = load_jsonl(args.agent_state)
        pending, skipped = classify_results(results, evaluated)
        for item in skipped:
            instance_id = item["instance_id"]
            evaluated[instance_id] = {
                "instance_id": instance_id,
                "status": "skipped_empty_patch",
                "resolved": False,
                "agent_status": item.get("status"),
                "patch_bytes": item.get("patch_bytes", 0),
            }
            save_jsonl(state_path, evaluated)
            print(json.dumps(evaluated[instance_id], ensure_ascii=False), flush=True)
        for item in pending:
            print(f"evaluating={item['instance_id']}", flush=True)
            evaluation = evaluate_instance(item, args)
            evaluated[item["instance_id"]] = evaluation
            save_jsonl(state_path, evaluated)
            print(json.dumps(evaluation, ensure_ascii=False), flush=True)
        if len(evaluated) < args.expected:
            time.sleep(args.poll_seconds)
    print(f"complete={len(evaluated)} state={state_path}", flush=True)


if __name__ == "__main__":
    main()
