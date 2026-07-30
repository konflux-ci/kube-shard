#!/usr/bin/env python3
"""Generate a PipelineRun YAML of a target size with N tasks.

Usage:
    python3 generate-pipelinerun.py [--size-kb 100] [--tasks 10] [--namespace default] -o output.yaml
"""
import argparse
import base64
import os


def main():
    parser = argparse.ArgumentParser(description="Generate a large PipelineRun YAML")
    parser.add_argument("--size-kb", type=int, default=100, help="Target size in KB (default: 100)")
    parser.add_argument("--tasks", type=int, default=10, help="Number of tasks in the PipelineRun (default: 10)")
    parser.add_argument("--namespace", type=str, default="default", help="Namespace (default: default)")
    parser.add_argument("--prefix", type=str, default="load-test-", help="generateName prefix (default: load-test-)")
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

    for t in range(args.tasks):
        lines.extend([
            f"    - name: task-{t}",
            "      taskSpec:",
            "        steps:",
            "        - name: step-0",
            "          image: busybox",
            f'          command: ["echo", "task-{t}-done"]',
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
    print(f"Generated {len(yaml)} bytes ({len(yaml)/1024:.1f} KB) with {args.tasks} tasks and {i} padding params")


if __name__ == "__main__":
    main()
