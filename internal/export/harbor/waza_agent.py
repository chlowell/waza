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

from harbor.agents.base import BaseAgent
from harbor.models.agent.context import AgentContext
from harbor.environments.base import BaseEnvironment
from harbor.models.trajectories import (
    Agent,
    FinalMetrics,
    Observation,
    ObservationResult,
    Step,
    ToolCall,
    Trajectory,
)

class WazaAgent(BaseAgent):
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

    async def setup(self, environment: BaseEnvironment) -> None:
        """No setup needed since the container image already has waza and the eval config."""
        pass

    def version(self) -> str:
        """Get waza version by running `waza --version`."""
        return "dev"

    @property
    def _install_agent_template_path(self) -> Path:
        return Path(__file__).parent / "install-waza.sh.j2"

    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        """Run waza against the pre-baked eval config in /waza/.

        The container already has /waza/eval.yaml (with graders) and
        /waza/task.yaml (with the prompt and expectations) from the export.
        We just need to run waza with the Copilot SDK executor.
        """

        token = os.environ.get("COPILOT_GITHUB_TOKEN")
        if not token:
            raise ValueError("COPILOT_GITHUB_TOKEN is not set in the environment")

        env = { "COPILOT_GITHUB_TOKEN": token }

        model = self.model_name or ""
        if "/" in model:
            model = model.split("/", 1)[-1]

        model_flag = f"--model {shlex.quote(model)}" if model else ""

        run_cmd = (
            "waza run /waza/eval.yaml "
            "--context-dir /waza/fixtures "
            f"{model_flag} "
            "--output /logs/artifacts/waza-results.json "
            "--skip-graders "
            "-v "
            "2>&1 | tee /logs/artifacts/waza-output.txt "
        )

        result = await environment.exec(run_cmd, env=env, timeout_sec=60)
        if result.return_code != 0:
            print(f"Waza run failed with code {result.return_code}: {result.stdout}")

        results_file = self.logs_dir / "waza-results.json"
        await environment.download_file("/logs/artifacts/waza-results.json", results_file)

        trajectory = self._transcript_to_trajectory(results_file)
        if trajectory:
            trajectory_path = self.logs_dir / "trajectory.json"
            trajectory_path.write_text(json.dumps(trajectory.dict(), indent=2))

    def _transcript_to_trajectory(self, results_file: Path) -> Trajectory | None:
        """Convert waza EvaluationOutcome JSON to a Harbor ATIF trajectory.

        Parses the transcript events from the first run of the first task
        and maps Copilot SDK session events to ATIF steps.
        """
        try:
            data = json.loads(results_file.read_text())
        except (json.JSONDecodeError, OSError) as exc:
            print(f"Failed to read waza results: {exc}")
            return None

        # Find the first task's first run with transcript data
        tasks = data.get("tasks", [])
        if not tasks:
            print("No tasks found in waza results")
            return None

        runs = tasks[0].get("runs", [])
        if not runs:
            print("No runs found in waza results")
            return None

        run = runs[0]
        events = run.get("transcript", [])
        session = run.get("session_digest", {})

        summary = data.get("summary", {})
        usage = summary.get("usage", {})

        steps: list[Step] = []
        step_id = 0

        # Track pending tool calls to pair starts with completions
        pending_tools: dict[str, dict] = {}

        for evt in events:
            evt_type = evt.get("type", "")

            if evt_type == "user.message":
                step_id += 1
                steps.append(Step(
                    step_id=step_id,
                    source="user",
                    message=evt.get("content") or evt.get("message") or "",
                ))

            elif evt_type == "system.message":
                step_id += 1
                steps.append(Step(
                    step_id=step_id,
                    source="system",
                    message=evt.get("content") or evt.get("message") or "",
                ))

            elif evt_type == "assistant.message" or evt_type == "assistant.reasoning":
                step_id += 1

                steps.append(Step(
                    step_id=step_id,
                    source="agent",
                    message=evt.get("content") or evt.get("reasoningText") or "",
                    model_name=self.model_name,
                ))

            elif evt_type == "tool.execution_start":
                call_id = evt.get("tool_call_id") or ""
                tool_name = evt.get("tool_name") or ""
                args = evt.get("arguments") or {}
                if isinstance(args, str):
                    try:
                        args = json.loads(args)
                    except json.JSONDecodeError:
                        args = {"raw": args}

                pending_tools[call_id] = {
                    "tool_name": tool_name,
                    "arguments": args,
                }

            elif evt_type in ("tool.execution_complete", "tool.execution_partial_result"):
                call_id = evt.get("tool_call_id") or ""
                tool_info = pending_tools.pop(call_id, {})
                tool_name = tool_info.get("tool_name") or evt.get("tool_name") or ""
                args = tool_info.get("arguments") or {}

                # Extract result content
                result_content = None
                tool_result = evt.get("tool_result")
                if isinstance(tool_result, dict):
                    parts = []
                    if tool_result.get("stdout"):
                        parts.append(tool_result["stdout"])
                    if tool_result.get("stderr"):
                        parts.append(f"[stderr] {tool_result['stderr']}")
                    result_content = "\n".join(parts) if parts else None
                elif isinstance(tool_result, str):
                    result_content = tool_result

                tool_call = ToolCall(
                    tool_call_id=call_id,
                    function_name=tool_name,
                    arguments=args if isinstance(args, dict) else {},
                )

                observation = Observation(
                    results=[ObservationResult(
                        source_call_id=call_id,
                        content=result_content,
                    )]
                ) if result_content is not None else None

                step_id += 1
                steps.append(Step(
                    step_id=step_id,
                    source="agent",
                    message=f"Executed {tool_name}",
                    tool_calls=[tool_call],
                    observation=observation,
                    model_name=self.model_name,
                ))

        final_output = run.get("final_output")
        if final_output:
            step_id += 1
            steps.append(Step(
                step_id=step_id,
                source="agent",
                message=final_output,
                model_name=self.model_name,
            ))

        return Trajectory(
            schema_version="ATIF-v1.2",
            session_id=session.get("session_id", "unknown"),
            agent=Agent(
                name="waza",
                version=self.version() or "unknown",
                model_name=self.model_name,
            ),
            steps=steps,
            final_metrics=FinalMetrics(
                total_prompt_tokens=usage.get("input_tokens"),
                total_completion_tokens=usage.get("output_tokens"),
                total_cached_tokens=usage.get("cache_read_tokens"),
                total_cost_usd=None,
                total_steps=len(steps),
            ),
        )