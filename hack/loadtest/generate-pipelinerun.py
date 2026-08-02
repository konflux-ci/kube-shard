#!/usr/bin/env python3
"""Generate a PipelineRun YAML of a target size with N tasks.

Usage:
    python3 generate-pipelinerun.py [--size-kb 100] [--tasks 10] [--namespace default] -o output.yaml
"""
import argparse
import base64
import os
import random


def main():
    parser = argparse.ArgumentParser(description="Generate a large PipelineRun YAML")
    parser.add_argument("--size-kb", type=int, default=100, help="Target size in KB (default: 100)")
    parser.add_argument("--tasks", type=int, default=10, help="Number of tasks in the PipelineRun (default: 10)")
    parser.add_argument("--namespace", type=str, default="default", help="Namespace (default: default)")
    parser.add_argument("--prefix", type=str, default="load-test-", help="generateName prefix (default: load-test-)")
    parser.add_argument("--image", type=str, default="registry.access.redhat.com/hi/core-runtime:1781714135", help="Step container image")
    parser.add_argument("--task-padding-kb", type=int, default=5, help="Random data padding per task in KB (default: 5)")
    parser.add_argument("-o", "--output", type=str, required=True, help="Output YAML file path")
    args = parser.parse_args()

    lines = [
        "apiVersion: tekton.dev/v1",
        "kind: PipelineRun",
        "metadata:",
        f"  generateName: {args.prefix}",
        f"  namespace: {args.namespace}",
        "spec:",
        "  pipelineSpec:",
        "    tasks:",
    ]

    total_steps = 0
    for t in range(args.tasks):
        num_steps = random.randint(1, 5)
        total_steps += num_steps
        padding = base64.b64encode(os.urandom(args.task_padding_kb * 750)).decode()
        lines.extend([
            f"    - name: task-{t}",
            "      taskSpec:",
            "        steps:",
        ])
        for s in range(num_steps):
            sleep_secs = random.randint(20, 300)
            lines.extend([
                f"        - name: step-{s}",
                f"          image: {args.image}",
                "          script: |",
                f"            DATA=\"{padding}\"",
                f"            echo \"task-{t} step-{s}: sleeping {sleep_secs}s with ${{#DATA}} bytes of payload\"",
                f"            sleep {sleep_secs}",
            ])

    lines.append("  params:")
    target = args.size_kb * 1000
    current = len("\n".join(lines)) + 1
    i = 0
    while current < target - 300:
        chunk = base64.b64encode(os.urandom(150)).decode()
        param = f"  - name: p-{i}\n    value: {chunk}"
        lines.append(param)
        current += len(param) + 1
        i += 1

    yaml = "\n".join(lines) + "\n"
    with open(args.output, "w") as f:
        f.write(yaml)
    print(f"Generated {len(yaml)} bytes ({len(yaml)/1024:.1f} KB) with {args.tasks} tasks, {total_steps} steps, and {i} padding params")


if __name__ == "__main__":
    main()
