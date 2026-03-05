"""Harbor installed agent that uses waza (Copilot SDK) to solve tasks.

Usage:
    harbor run -p <task-or-dataset> \
        --agent-import-path harbor_tasks.agent.waza_agent:WazaAgent \
        -m copilot/gpt-4o

Environment variables:
    COPILOT_GITHUB_TOKEN  Copilot SDK authentication token (set by Harbor)
"""

import json
import os
import shlex
import textwrap
from pathlib import Path

from harbor.agents.installed.base import BaseInstalledAgent, ExecInput
from harbor.models.agent.context import AgentContext


class WazaAgent(BaseInstalledAgent):
    """A Harbor agent backed by the waza CLI and Copilot SDK."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

    @staticmethod
    def name() -> str:
        return "waza"

    @property
    def _install_agent_template_path(self) -> Path:
        return Path(__file__).parent / "install-waza.sh.j2"

    def populate_context_post_run(self, context: AgentContext) -> None:
        """Read waza results JSON and populate Harbor context with token metrics."""
        results_path = self.logs_dir / "waza-results.json"
        if not results_path.exists():
            print("No waza results file found; skipping metric population")
            return

        try:
            data = json.loads(results_path.read_text())
        except (json.JSONDecodeError, OSError) as exc:
            print(f"Failed to read waza results: {exc}")
            return

        digest = data.get("digest", {})
        usage = digest.get("usage", {})
        if usage:
            context.n_input_tokens = usage.get("tokens_in", 0)
            context.n_output_tokens = usage.get("tokens_out", 0)

    def create_run_agent_commands(self, instruction: str) -> list[ExecInput]:
        """Generate commands to run waza with the Copilot SDK against the task."""
        escaped_instruction = shlex.quote(instruction)

        env = {
            "COPILOT_GITHUB_TOKEN": os.environ.get("COPILOT_GITHUB_TOKEN", ""),
        }

        # Strip provider prefix from model name (e.g. "copilot/gpt-4o" -> "gpt-4o")
        model = self.model_name or ""
        if "/" in model:
            model = model.split("/", 1)[-1]

        model_flag = f"--model {shlex.quote(model)}" if model else ""

        # Generate a minimal eval spec on the fly inside the container.
        # The eval has one task whose prompt is the Harbor instruction.
        eval_yaml = textwrap.dedent("""\
            name: harbor-task
            config:
              trials_per_task: 1
              timeout_seconds: 600
              executor: copilot-sdk
            tasks:
              - task.yaml
        """)

        task_yaml = textwrap.dedent(f"""\
            id: harbor-task-001
            name: Harbor Task
            inputs:
              prompt: {escaped_instruction}
        """)

        setup_cmd = (
            "mkdir -p /tmp/waza-eval /logs/agent/transcripts && "
            f"cat > /tmp/waza-eval/eval.yaml << 'WAZA_EVAL_EOF'\n{eval_yaml}WAZA_EVAL_EOF\n"
            f"cat > /tmp/waza-eval/task.yaml << 'WAZA_TASK_EOF'\n{task_yaml}WAZA_TASK_EOF"
        )

        run_cmd = (
            "waza run /tmp/waza-eval/eval.yaml "
            f"{model_flag} "
            "--output /logs/agent/waza-results.json "
            "--transcript-dir /logs/agent/transcripts "
            "-v "
            "2>&1 | tee /logs/agent/waza-output.txt"
        )

        return [
            ExecInput(command=setup_cmd, env=env),
            ExecInput(command=run_cmd, env=env, timeout_sec=660),
        ]
