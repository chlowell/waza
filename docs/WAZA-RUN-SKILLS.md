# How `waza run` Makes Skills Available to Agents

This note describes the execution path that makes skills available during `waza run`, with emphasis on the Copilot engine.

## Summary

`waza run` does not discover skills by recursively scanning the agent workspace.

Instead, it makes skills available through two explicit mechanisms:

1. It passes a list of skill directories into the execution request.
2. The Copilot engine converts those directories into:
   - `SkillDirectories` on the Copilot session config
   - an appended system message that lists available skills from `SKILL.md`

Both mechanisms expect directories that contain `SKILL.md` directly.

## Flow

### 1. Eval config provides `skill_directories`

In the benchmark spec, skill directories come from:

```yaml
config:
  skill_directories:
    - ./skills/code-explainer
    - ../shared-skills/azure-prepare
```

These values are loaded into `BenchmarkSpec.Config.SkillPaths`.

### 2. `waza run` resolves skill paths relative to the spec directory

When the runner builds an execution request, it resolves `config.skill_directories` relative to the directory containing the eval spec.

Conceptually:

```go
resolvedSkillPaths := utils.ResolvePaths(spec.Config.SkillPaths, r.cfg.SpecDir())
```

That resolved list is attached to the execution request as `ExecutionRequest.SkillPaths`.

### 3. The execution request carries both source and skill paths

The request sent to the execution engine includes:

- `SkillPaths`: explicit directories from `skill_directories`
- `SourceDir`: the run source directory when provided

The source directory matters because the Copilot engine treats it as an additional skill search directory.

### 4. The Copilot engine builds the final skill directory list

Before creating a session, the Copilot engine builds a directory list like this:

1. Start with the source directory
2. Append `ExecutionRequest.SkillPaths`
3. Remove duplicates

Conceptually:

```go
skillDirs := []string{cwd}
skillDirs = append(skillDirs, req.SkillPaths...)
```

This list is then used in two places.

## Mechanism 1: Copilot `SkillDirectories`

The Copilot engine passes the final directory list into the SDK session config:

```go
sessionConfig.SkillDirectories = skillDirs
```

That means the Copilot SDK gets explicit directories to treat as skill locations.

## Mechanism 2: Appended System Message

The Copilot engine also scans those same directories and builds a system message describing available skills.

For each directory in `skillDirs`, it checks only:

```text
<dir>/SKILL.md
```

If `SKILL.md` exists and contains YAML frontmatter with `name` and optionally `description`, the engine appends XML-like skill metadata to the system message:

```xml
<available_skills>
<skill>
  <name>code-explainer</name>
  <description>Explain source code clearly</description>
</skill>
</available_skills>
```

This is additive guidance for the model. It does not replace `SkillDirectories`; both are provided.

## Important Constraint

Skill loading is not recursive in this path.

The runtime checks whether each configured directory itself contains `SKILL.md`. It does not walk nested trees like:

```text
/app/skills/code-explainer/SKILL.md
```

unless the configured skill directory is exactly:

```text
/app/skills/code-explainer
```

Configuring only `/app/.github/skills` is not sufficient for the current implementation.

## Required Skills Validation

Before running tests, `waza run` validates `required_skills` if they are configured.

That validation uses the same assumption:

- each entry in `skill_directories` must point to a directory containing `SKILL.md` directly
- discovery is not recursive

If `required_skills` is set but `skill_directories` is empty, the run fails before execution.

## What Does Not Provide Skills

These things do not, by themselves, make a skill available to the agent:

- copying a skill somewhere into the workspace without adding its directory to `skill_directories`
- placing skills under a parent folder and expecting recursive discovery
- fixture files copied into the temp workspace

Resource files and skill directories are separate concepts in `waza run`.

## Practical Rule

If you need a skill to be available during `waza run`, ensure that at least one of these is true:

1. The execution source directory itself contains `SKILL.md`.
2. `config.skill_directories` contains the exact directory that contains `SKILL.md`.

For exported or containerized environments, the embedded eval config should usually rewrite `skill_directories` to in-container paths that point directly at the staged skill directories.

## Harbor Export Implication

For Harbor export specifically, making only the primary skill available is not sufficient.

If the source eval uses `config.skill_directories`, every configured source skill directory must be:

1. copied into the Harbor image build context
2. given a deterministic in-container path that directly contains `SKILL.md`
3. rewritten into the embedded Harbor `eval.yaml` under `config.skill_directories`

That includes shared or sibling skills referenced by the source eval, not just the directory containing the eval under test.