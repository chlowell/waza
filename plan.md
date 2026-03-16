# Design: Running Waza Tasks as Harbor Tasks

## Problem Statement

Waza is a CLI for evaluating agent skills (prompt → agent → grade response). Harbor is a framework for evaluating agent performance in containerized environments (instruction → agent terminal interaction → test verification). We want to enable running waza tasks as Harbor tasks so waza eval suites can be benchmarked in Harbor's infrastructure with any Harbor-compatible agent.

## Waza Project Layout (Reference)

### Example Project Structure

A waza eval project (e.g., `examples/code-explainer/`) looks like:

```
code-explainer/
├── eval.yaml                    # BenchmarkSpec — the top-level eval configuration
├── SKILL.md                     # Skill documentation (optional)
├── tasks/                       # TestCase YAML files (one per task)
│   ├── explain-python-recursion.yaml
│   ├── explain-js-async.yaml
│   ├── explain-list-comprehension.yaml
│   └── explain-sql-join.yaml
├── fixtures/                    # Shared input files referenced by tasks
│   ├── factorial.py
│   ├── fetch_user.js
│   ├── squares.py
│   └── user_orders.sql
└── graders/                     # Custom grader scripts (optional)
    └── explanation_quality.py
```

### eval.yaml (BenchmarkSpec)

The top-level config file defines the eval suite. Key sections:

```yaml
# Identity
name: code-explainer-eval
description: |
  Evaluation suite for the code-explainer skill.
skill: code-explainer
version: "1.0"

# Execution config
config:
  trials_per_task: 3          # Runs per task (for consistency measurement)
  timeout_seconds: 300        # Max execution time per task
  parallel: false             # Sequential vs concurrent execution
  executor: mock              # Engine type: mock, copilot-sdk
  model: claude-sonnet-4-20250514
  judge_model: gpt-4o         # Model for prompt graders (LLM-as-judge)

# Global graders — applied to ALL tasks
graders:
  - type: code
    name: has_explanation
    config:
      assertions:
        - "len(output) > 10"
  - type: text
    name: no_errors
    config:
      regex_not_match:
        - "(?i)fatal error|crashed|exception occurred"

# Task file globs — resolved relative to eval.yaml directory
tasks:
  - "tasks/*.yaml"

# Aggregate metrics
metrics:
  - name: task_completion
    weight: 0.4
    threshold: 0.8
```

**Go struct** (`internal/models/spec.go`):
```go
type BenchmarkSpec struct {
    SpecIdentity     `yaml:",inline"`
    SkillName        string          `yaml:"skill"`
    Version          string          `yaml:"version"`
    Config           Config          `yaml:"config"`
    Graders          []GraderConfig  `yaml:"graders"`
    Metrics          []MeasurementDef `yaml:"metrics"`
    Tasks            []string        `yaml:"tasks"`     // Glob patterns
    Baseline         bool            `yaml:"baseline"`
    // ... hooks, inputs, etc.
}

type GraderConfig struct {
    Kind       GraderKind     `yaml:"type"`      // "code", "text", "prompt", etc.
    Identifier string         `yaml:"name"`
    ScriptPath string         `yaml:"script"`
    Rubric     string         `yaml:"rubric"`    // For prompt graders
    ModelID    string         `yaml:"model"`     // For prompt graders
    Weight     float64        `yaml:"weight"`    // Score contribution (default 1.0)
    Parameters map[string]any `yaml:"config"`    // Type-specific params
}
```

### TestCase YAML (One Per Task)

Each task file defines a single test case with inputs, expected outputs, and optional task-specific graders:

```yaml
# Example: tasks/explain-python-recursion.yaml
id: explain-python-recursion-001       # Unique identifier
name: Explain Python Recursion          # Display name
description: |                          # What this task tests
  Test explaining a recursive factorial function in Python.
  The skill should identify the recursive pattern and base case.

tags:                                   # Labels for filtering
  - python
  - recursion
  - beginner

inputs:                                 # STIMULUS — what the agent receives
  prompt: "Explain this code to me"     # The main prompt/instruction
  context:                              # Key-value metadata
    language: python
    complexity: beginner
    concept: recursion
  files:                                # Fixture file references
    - path: factorial.py                # Resolved from fixtures/ directory

expected:                               # EXPECTATIONS — how to verify success
  output_contains:                      # Keywords that MUST appear in response
    - "recursive"
    - "factorial"
  output_contains_any:                  # Alternative keywords (any one satisfies)
    - "base case"
    - "n <= 1"
    - "termination"
  output_not_contains: []               # Keywords that must NOT appear
  outcomes:
    - type: task_completed
  behavior:                             # Agent behavior constraints
    max_tool_calls: 5
    max_response_time_ms: 30000

graders:                                # TASK-SPECIFIC graders (in addition to global)
  - name: explains_recursion
    type: code
    config:
      assertions:
        - "len(output) > 10"
```

**Go struct** (`internal/models/testcase.go`):
```go
type TestCase struct {
    Active      *bool             `yaml:"enabled,omitempty"`     // nil = enabled
    ContextRoot string            `yaml:"context_dir,omitempty"` // Override fixture dir
    DisplayName string            `yaml:"name"`
    TestID      string            `yaml:"id"`
    Summary     string            `yaml:"description,omitempty"`
    Tags        []string          `yaml:"tags,omitempty"`
    TimeoutSec  *int              `yaml:"timeout_seconds,omitempty"`
    Stimulus    TestStimulus       `yaml:"inputs"`
    Expectation TestExpectation   `yaml:"expected,omitempty"`
    Validators  []ValidatorInline `yaml:"graders,omitempty"`
}

type TestStimulus struct {
    Message     string            `yaml:"prompt"`           // Main prompt text
    Metadata    map[string]any    `yaml:"context,omitempty"` // Context key-values
    Resources   []ResourceRef     `yaml:"files,omitempty"`   // Fixture file refs
    Environment map[string]string `yaml:"environment,omitempty"`
}

type ResourceRef struct {
    Location string `yaml:"path,omitempty"`     // File path relative to fixtures/
    Body     string `yaml:"content,omitempty"`  // Inline content (alternative)
}

type TestExpectation struct {
    OutcomeSpecs    []OutcomeSpec  `yaml:"outcomes,omitempty"`
    ToolPatterns    map[string]any `yaml:"tool_calls,omitempty"`
    BehaviorRules   BehaviorRules  `yaml:"behavior,omitempty"`
    MustInclude     []string       `yaml:"output_contains,omitempty"`
    MustExclude     []string       `yaml:"output_not_contains,omitempty"`
    ExpectedTrigger *bool          `yaml:"should_trigger,omitempty"`
}

type BehaviorRules struct {
    MaxToolInvocations int      `yaml:"max_tool_calls,omitempty"`
    MaxRounds          int      `yaml:"max_iterations,omitempty"`
    MaxTokens          int      `yaml:"max_tokens,omitempty"`
    MustUseTool        []string `yaml:"required_tools,omitempty"`
    ForbidTool         []string `yaml:"forbidden_tools,omitempty"`
}

type ValidatorInline struct {
    Identifier string         `yaml:"name"`
    Kind       GraderKind     `yaml:"type,omitempty"`
    Checks     []string       `yaml:"assertions,omitempty"`
    Rubric     string         `yaml:"rubric,omitempty"`
    Weight     float64        `yaml:"weight,omitempty"`
    Parameters map[string]any `yaml:"config,omitempty"`
}
```

### Fixtures

Plain data files (code snippets, documents, etc.) in the `fixtures/` directory. Referenced by tasks via `inputs.files[].path`. Examples:

- `factorial.py`: `def factorial(n): ...` (4 lines)
- `fetch_user.js`: `async function fetchUserData(userId) { ... }` (11 lines)
- `squares.py`: `result = [x**2 for x in range(20) if x % 3 == 0]` (1 line)
- `user_orders.sql`: `SELECT u.name, o.total ... INNER JOIN ... LIMIT 10;` (6 lines)

Fixtures are loaded by the runner from `--context-dir` (default: `./fixtures` relative to eval.yaml). Each task execution gets a fresh temp workspace with fixtures copied in.

### Grader Types

| GraderKind | YAML `type:` | What It Does | Config Keys |
|---|---|---|---|
| InlineScript | `code` | Runs Python/JS assertions against output | `assertions: [...]`, `language: python\|javascript` |
| Text | `text` | Substring/regex matching on output | `contains`, `not_contains`, `contains_cs`, `not_contains_cs`, `regex_match`, `regex_not_match` |
| Prompt | `prompt` | LLM-as-judge evaluation | `prompt` (rubric), `model`, `mode: independent\|pairwise` |
| File | `file` | Checks workspace file existence/content | `must_exist: [...]`, `content_patterns: {...}` |
| JSONSchema | `json_schema` | Validates JSON output against schema | `schema: {...}`, `strict: bool` |
| Program | `program` | Runs external grader script | `script: path`, language-specific config |
| Behavior | `behavior` | Agent behavior constraints | `max_tool_calls`, `max_tokens`, `required_tools` |
| Diff | `diff` | File comparison with expected snapshots | `expected_files: [...]` |
| ToolConstraint | `tool_constraint` | Validates tool usage patterns | `allowed_tools`, `forbidden_tools`, `min_calls`, `max_calls` |

**Grading execution flow:**
1. Runner invokes `graders.Create(kind, name, params)` for each GraderConfig
2. Each grader receives context: `{output, transcript, tool_calls, task, ...}`
3. Returns `GraderResults{Score: 0.0-1.0, Passed: bool, Feedback: string, ...}`
4. Scores are weighted and aggregated across all graders

### How `waza run` Orchestrates

```
waza run eval.yaml --context-dir fixtures/ -v
    │
    ├─ Load eval.yaml → BenchmarkSpec
    ├─ Resolve task globs → load task YAML files → []TestCase
    ├─ Create AgentEngine (mock or copilot-sdk)
    ├─ For each TestCase:
    │   ├─ Load fixtures from context-dir into temp workspace
    │   ├─ For each trial (1..trials_per_task):
    │   │   ├─ engine.Execute(prompt + fixtures) → response
    │   │   ├─ Run global graders + task graders → []GraderResults
    │   │   └─ Compute weighted score
    │   └─ Aggregate: pass rate, avg score, std dev, confidence intervals
    └─ Produce EvaluationOutcome (JSON) with per-task results
```

## Harbor Task Layout (Reference)

A Harbor task has this structure:

```
task-name/
├── instruction.md         # Markdown instruction given to the agent
├── task.toml              # Configuration and metadata
├── environment/
│   └── Dockerfile         # Container environment definition
├── solution/              # Optional — for Oracle agent validation
│   └── solve.sh
└── tests/
    └── test.sh            # Verification script — must write reward to /logs/verifier/
```

**Key Harbor concepts:**
- Agent interacts with container via terminal (not a structured API)
- `instruction.md` tells the agent what to do
- `test.sh` runs AFTER the agent finishes, verifies work, writes `reward.txt` or `reward.json` to `/logs/verifier/`
- A **dataset** is a directory of task directories, run via `harbor run -p <dataset-dir> -a <agent>`

## Concept Mapping

| Waza Concept | Harbor Concept | Mapping Strategy |
|---|---|---|
| `TestCase.inputs.prompt` | `instruction.md` | Task prompt becomes the agent instruction |
| `TestCase.inputs.files` (fixtures) | Files in environment or instruction inline | Fixture content embedded in instruction or pre-loaded in container |
| Graders (code, text, prompt, etc.) | `tests/test.sh` | Waza graders run inside test.sh to produce reward |
| `eval.yaml` (BenchmarkSpec) | Dataset directory | One eval.yaml → one Harbor dataset (directory of tasks) |
| `eval.yaml` config | `task.toml` | Timeout, metadata mapped to Harbor config |
| Each `TestCase` | One Harbor task subdirectory | 1:1 mapping from waza task → Harbor task |
| `TestCase.expected` | Verification in test.sh | Expected keywords/patterns checked by waza graders |
| `Dockerfile` (existing) | `environment/Dockerfile` (per-task) | Waza binary embedded in each task's Dockerfile |
| Grader score (0.0-1.0) | `/logs/verifier/reward.json` | Waza grader scores written as Harbor reward metrics |
| No waza equivalent | `solution/solve.sh` | Optional; could generate stub or skip |

## Proposed Features

### Feature 1: `waza export` Command

A new CLI command that converts a waza eval project into an external format. The `--format` flag specifies the target format (initially `harbor`, extensible to others in the future).

```
waza export <eval.yaml> [flags]

Flags:
  --format, -f <format>     Output format (required). Supported: "harbor"
  --output-dir, -o <path>   Output directory (default: ./<format>-tasks/)
  --waza-binary <path>      Path to waza binary to embed (default: build from source)
  --base-image <image>      Base Docker image (default: ubuntu:24.04)
  --task <pattern>           Export only matching tasks (same filter as `waza run --task`)
  --tags <pattern>           Export only tasks matching tags
  --inline-fixtures          Embed fixture content in instruction (default: true for small files)
  --fixture-threshold <kb>   Max fixture size to inline (default: 10)
```

**Example:**
```bash
waza export examples/code-explainer/eval.yaml --format harbor -o ./harbor-dataset/
```

**What it produces (Harbor format):**

```
harbor-dataset/                        # Harbor dataset directory
├── explain-python-recursion-001/      # One dir per waza TestCase
│   ├── instruction.md                 # Generated from task prompt + fixtures
│   ├── task.toml                      # Generated from eval config + task metadata
│   ├── environment/
│   │   ├── Dockerfile                 # Ubuntu + waza binary + Python (for graders)
│   │   └── waza-config/              # Embedded waza task + grader configs
│   │       ├── eval.yaml             # Minimal eval spec (graders only)
│   │       ├── task.yaml             # The specific task definition
│   │       ├── fixtures/             # Fixture files for this task
│   │       └── graders/             # Custom grader scripts (if any)
│   ├── tests/
│   │   └── test.sh                   # Runs `waza grade` and writes reward
│   └── solution/                     # Optional
│       └── solve.sh                  # Stub or skip
├── explain-js-async-001/
│   ├── ...
└── explain-sql-join-001/
    ├── ...
```

### Feature 2: `waza grade` Command (Regrading Previous Runs)

The Harbor bridge depends on `waza grade` regrading the JSON output from a previous `waza run --output ...` invocation. Harbor's test.sh therefore grades the run artifact produced by the agent-side `waza run`, rather than passing raw agent output directly into `waza grade`.

```
waza grade <eval.yaml> --task <task-id> [flags]

Flags:
  --results <file>          Path to `waza run --output` JSON
  --workspace <path>        Agent workspace directory (for file graders)
  --judge-model <model>     Model override for prompt graders
  -o, --output <file>       Write merged `EvaluationOutcome` JSON
  -v, --verbose             Verbose grader logging on stderr
```

**What it does:**
1. Loads the eval.yaml and finds the specified task
2. Loads the matching run data from a previous `waza run --output` JSON file
3. Instantiates global graders (from eval.yaml) + task-specific graders
4. Re-runs the graders against the saved run output, transcript, and workspace
5. Computes weighted score and prints a `GradeOutcome` JSON document to stdout
6. Optionally writes a regraded `EvaluationOutcome` JSON file via `--output`

**Example usage in Harbor test.sh:**
```bash
#!/bin/sh

RESULTS="/logs/artifacts/waza-results.json"
GRADE_JSON="/logs/verifier/grade.json"
REWARD_FILE="/logs/verifier/reward.txt"

# Run waza graders against the saved run artifact
waza grade /waza/eval.yaml \
  --task "explain-python-recursion-001" \
  --workspace /app \
  --results "$RESULTS" \
  > "$GRADE_JSON"

# Harbor consumes a scalar reward, so extract overall_score separately.
score=$(awk -F: '/"overall_score"/ { gsub(/[ ,]/, "", $2); print $2; exit }' "$GRADE_JSON")
printf '%s\n' "${score:-0}" > "$REWARD_FILE"

exit 0
```

**Output format (grade.json):**
```json
{
  "overall_score": 0.85,
  "passed": true,
  "tasks": {
    "explain-python-recursion-001": {
      "overall_score": 0.85,
      "passed": true,
      "grader_averages": {
        "has_explanation": 0.9,
        "no_errors": 1.0,
        "explains_recursion": 0.7
      }
    }
  }
}
```

Harbor still consumes a scalar reward, so the exported verifier writes `/logs/verifier/reward.txt` from `overall_score` and keeps the full grading JSON separately for debugging.

### Feature 3: Generated Artifacts Detail

#### instruction.md Generation

The instruction is composed from:
1. **Task prompt** (`inputs.prompt`) — the primary instruction
2. **Fixture content** — for small files, inlined with code fences; for large files, referenced as pre-loaded paths
3. **Output expectation** — tells the agent where to write its response

**Example generated instruction.md:**
```markdown
# Explain Python Recursion

Explain this code to me

## Context

Language: python
Complexity: beginner
Concept: recursion

## Code to Explain

The following code is available at `/app/fixtures/factorial.py`:

\```python
def factorial(n):
    if n <= 1:
        return 1
    return n * factorial(n - 1)
\```

## Instructions

Write your explanation to `/app/response.md`.
```

#### task.toml Generation

Maps waza config to Harbor config:

```toml
version = "1.0"

[metadata]
source = "waza"
waza_eval = "code-explainer-eval"
waza_task_id = "explain-python-recursion-001"
difficulty = "beginner"                         # from task tags/context
category = "code-explanation"                   # from eval skill name
tags = ["python", "recursion", "beginner"]      # from task tags

[verifier]
timeout_sec = 300.0                            # from eval config.timeout_seconds

[agent]
timeout_sec = 300.0                            # from eval config.timeout_seconds

[environment]
build_timeout_sec = 600.0
cpus = 1
memory = "2G"
storage = "10G"
```

#### environment/Dockerfile Generation

```dockerfile
FROM ubuntu:24.04

WORKDIR /app

# Install dependencies for waza graders
RUN apt-get update && \
    apt-get install -y ca-certificates python3 && \
    rm -rf /var/lib/apt/lists/*

# Install waza binary
COPY waza /usr/local/bin/waza

# Copy waza configuration for grading
COPY waza-config/ /waza/

# Make staged skill directories discoverable to agents inside the workspace
COPY skills/ /app/skills/

# Pre-load fixtures in the workspace
COPY waza-config/fixtures/ /app/fixtures/
```

The `waza` binary is copied into `environment/` during export so Docker can COPY it.
The exported build context also includes staged skill directories under `skills/<skill-name>/`, copied into the container's WORKDIR. The embedded Harbor eval must rewrite `config.skill_directories` to those exact in-container directories so `waza run` can discover them without relying on recursive search.

#### tests/test.sh Generation

```bash
#!/bin/bash

# Waza-Harbor bridge: regrade the saved waza run artifact
# Task: explain-python-recursion-001

RESULTS="/logs/artifacts/waza-results.json"
GRADE_JSON="/logs/verifier/grade.json"
GRADE_LOG="/logs/verifier/grade-output.txt"
REWARD_FILE="/logs/verifier/reward.txt"

# Check if the agent produced a run artifact
if [ ! -f "$RESULTS" ]; then
  echo 0 > "$REWARD_FILE"
    exit 0
fi

# Run waza graders
waza grade /waza/eval.yaml \
    --task "explain-python-recursion-001" \
    --workspace /app \
  --results "$RESULTS" \
  -v > "$GRADE_JSON" 2> "$GRADE_LOG"

# Fallback: if waza grade failed, write zero reward
if [ $? -ne 0 ]; then
  echo 0 > "$REWARD_FILE"
  exit 0
fi

score=$(awk -F: '/"overall_score"/ { gsub(/[ ,]/, "", $2); print $2; exit }' "$GRADE_JSON")
printf '%s\n' "${score:-0}" > "$REWARD_FILE"
```

## Architecture

### Export Flow

```
waza export eval.yaml --format harbor --output-dir ./harbor-dataset/
    │
    ├─ Parse eval.yaml → BenchmarkSpec
    ├─ Resolve and load all task YAML files (from spec.Tasks globs)
    ├─ Load fixture files referenced by tasks
    ├─ Build or locate waza binary
    ├─ Select exporter based on --format flag
    │
    └─ HarborExporter.Export():
        └─ For each TestCase:
            ├─ Create task directory: harbor-dataset/<task-id>/
            ├─ Generate instruction.md
            │   ├─ Task prompt as heading/body
            │   ├─ Context metadata as section
            │   ├─ Inline small fixtures with code fences
            │   ├─ Reference large fixtures as paths
            │   └─ Add output instructions
            ├─ Generate task.toml
            │   ├─ Map waza config → Harbor config
            │   └─ Preserve task metadata (tags, context)
            ├─ Create environment/
            │   ├─ Generate Dockerfile
            │   ├─ Copy waza binary
            │   └─ Create waza-config/ with:
            │       ├─ Minimal eval.yaml (graders only)
            │       ├─ task.yaml
            │       ├─ fixtures/
            │       └─ graders/ (custom scripts)
            └─ Create tests/
                └─ Generate test.sh
```

### Grading Flow (at Harbor runtime)

```
Harbor starts container from environment/Dockerfile
    │
    ├─ Agent receives instruction.md
    ├─ Agent works in /app and writes `/logs/artifacts/waza-results.json`
    │  via `waza run --skip-graders --output ...`
    │
    └─ Harbor runs tests/test.sh
      ├─ test.sh invokes: waza grade /waza/eval.yaml --task <id> --results ...
        │   ├─ Loads eval.yaml (graders section)
      │   ├─ Loads task.yaml (task-specific graders + expected)
      │   ├─ Loads the saved run output, transcript, and metadata
      │   ├─ Instantiates graders (code, text, prompt, etc.)
      │   ├─ Re-runs grading against that saved run plus `/app`
      │   ├─ Writes GradeOutcome JSON to `/logs/verifier/grade.json`
      │   └─ Extracts `overall_score` into `/logs/verifier/reward.txt`
        └─ Exit 0
```

## Grader Type Handling in Harbor Context

| Waza Grader | Harbor Strategy | Notes |
|---|---|---|
| **text** | Runs directly via `waza grade` | Contains/regex checks on output text. No external deps. |
| **code** (inline script) | Runs via `waza grade` + Python/JS | Needs Python or Node in Dockerfile. Evaluates assertions against output. |
| **prompt** (LLM-as-judge) | Runs via `waza grade` + API access | Needs network access and API key. Set `allow_internet = true` in task.toml. Uses `[verifier] env` for API keys. |
| **file** | Runs via `waza grade` --workspace | Checks files in agent workspace. Natural fit for Harbor. |
| **program** | Runs via `waza grade` | Custom script executed in container. Script must be in waza-config/. |
| **behavior** | Limited in Harbor context | Tool call counts, timing not available. Partially supported. |
| **json_schema** | Runs via `waza grade` | Validates JSON output against schema. |

### Graders Requiring Special Handling

- **prompt grader**: Needs LLM API access. The Harbor container must have internet access and API credentials. The export command should set `allow_internet = true` and document required env vars.
- **behavior grader**: Waza tracks tool calls and response time from its own agent engine. In Harbor, the agent is controlled by Harbor's agent framework, not waza. Behavior graders may not have access to tool call data. **Recommendation:** Skip or warn about behavior graders during export.
- **skill_invocation / action_sequence**: These are waza-specific (Copilot Skills). Not applicable in Harbor context. Skip during export.

## Edge Cases and Considerations

### Agent Output Capture
- **Text tasks** (explanations, summaries): Agent writes to `/app/response.md`
- **File tasks** (code generation): Agent creates/modifies files in `/app/`; file grader checks them
- **Mixed tasks**: test.sh captures both file state and any response file
- The instruction.md must clearly tell the agent WHERE to put output

### Fixture Handling
- **Small fixtures** (< threshold): Inlined in instruction.md for maximum visibility
- **Large fixtures**: Pre-loaded in `/app/fixtures/` and referenced by path in instruction
- All fixtures also in `/waza/fixtures/` for grader reference

### Multi-trial Support
- Waza supports `trials_per_task` for consistency measurement
- Harbor runs each task once per agent invocation
- For multi-trial in Harbor, run the same dataset multiple times
- Export should set `trials_per_task: 1` in the embedded eval.yaml

### Global vs Task-Specific Graders
- Global graders (from eval.yaml) apply to all tasks
- Task-specific graders (from task.yaml) apply to one task
- The embedded eval.yaml in each Harbor task includes BOTH
- `waza grade` runs all applicable graders

## Implementation Plan

### Phase 1: `waza grade` Command
Add standalone grading capability to the waza CLI.

**New files:**
- `cmd/waza/cmd_grade.go` — CLI command definition
- `internal/grading/standalone.go` — Standalone grading logic (reuses existing grader infrastructure)

**Changes:**
- `cmd/waza/main.go` — Register grade command

### Phase 2: `waza export` Command with Exporter Interface
Add extensible export capability with `--format` flag.

**New files:**
- `cmd/waza/cmd_export.go` — CLI command with `--format` flag dispatch
- `internal/export/exporter.go` — `Exporter` interface and registry
- `internal/export/harbor/exporter.go` — Harbor format exporter (implements Exporter)
- `internal/export/harbor/instruction.go` — instruction.md generation
- `internal/export/harbor/tasktoml.go` — task.toml generation
- `internal/export/harbor/dockerfile.go` — Dockerfile generation
- `internal/export/harbor/testscript.go` — test.sh generation
- `internal/export/harbor/templates/` — Go templates for generated files

**Changes:**
- `cmd/waza/main.go` — Register export command

**Exporter interface (extensible for future formats):**
```go
type Exporter interface {
    Format() string
    Export(ctx context.Context, spec *models.BenchmarkSpec, tasks []models.TestCase, opts ExportOptions) error
}
```

### Phase 3: Testing and Documentation
- Unit tests for all export logic
- Integration test: export code-explainer example, verify Harbor structure
- Documentation in site/
- Update README.md with Harbor integration guide

## Open Questions

1. **Output location convention**: Should we standardize on `/app/response.md` for text output, or make it configurable per task? Some tasks might produce structured output (JSON) vs prose.

2. **Prompt grader credentials**: How should API keys for LLM-as-judge be passed? Via Harbor's `[verifier] env` config is natural, but needs documentation.

3. **Solution generation**: Should `waza harbor export` generate `solution/solve.sh` stubs? For text tasks, a mock response could serve as the "solution" for Oracle agent validation.

4. **Shared Docker image**: Should we publish a `waza` Docker image to a registry so Harbor Dockerfiles can `FROM waza:latest` instead of copying the binary? This would reduce dataset size but add a distribution dependency.

5. **Behavior graders**: Should we drop them silently, warn, or attempt partial support? Harbor agents don't expose tool call metadata the same way waza's AgentEngine does.

6. **Dataset metadata**: Should the export also produce a Harbor `registry.json` or dataset manifest for registered dataset support?
