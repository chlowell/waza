"""Harbor installed agent that uses waza (Copilot SDK) to solve tasks.

Expects the container to have waza eval configuration pre-loaded at /waza/
(eval.yaml, task.yaml, fixtures/) as produced by `waza export -f harbor`.
The agent runs `waza run` against this configuration using the Copilot SDK,
which sends the task prompt to a Copilot-backed LLM and captures the response.

Usage:
    harbor run -p <task-or-dataset> \\
        --agent-import-path agent.waza_agent:WazaAgent \\
        -m copilot/gpt-4o

Environment variables:
    COPILOT_GITHUB_TOKEN  Copilot SDK authentication token (set by Harbor)
"""

import json
import os
import shlex
from pathlib import Path

from harbor.agents.installed.base import BaseInstalledAgent, ExecInput
from harbor.models.agent.context import AgentContext


class WazaAgent(BaseInstalledAgent):
    """A Harbor agent backed by the waza CLI and Copilot SDK.

    The exported Harbor task already contains a complete waza eval configuration
    at /waza/ (eval.yaml with graders, task.yaml with the prompt, fixtures/).
    This agent simply runs `waza run` against that configuration so the Copilot
    SDK agent processes the task and writes output for the graders to evaluate.
    """

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
        """Run waza against the pre-baked eval config in /waza/.

        The container already has /waza/eval.yaml (with graders) and
        /waza/task.yaml (with the prompt and expectations) from the export.
        We just need to run waza with the Copilot SDK executor.
        """
        env = {
            "COPILOT_GITHUB_TOKEN": os.environ.get("COPILOT_GITHUB_TOKEN", ""),
        }

        # Strip provider prefix from model name (e.g. "copilot/gpt-4o" -> "gpt-4o")
        model = self.model_name or ""
        if "/" in model:
            model = model.split("/", 1)[-1]

        model_flag = f"--model {shlex.quote(model)}" if model else ""

        run_cmd = (
            "waza run /waza/eval.yaml "
            "--context-dir /waza/fixtures "
            f"{model_flag} "
            "--output /logs/artifacts/waza-results.json "
            "-v "
            "2>&1 | tee /logs/artifacts/waza-output.txt"
        )

        return [
            ExecInput(command=run_cmd, env=env, timeout_sec=660),
        ]
