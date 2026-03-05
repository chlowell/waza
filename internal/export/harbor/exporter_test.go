package harbor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/export"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// projectRoot returns the waza project root based on the test file location.
func projectRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(filename), "..", "..", "..")
}

func TestGenerateInstruction(t *testing.T) {
	root := projectRoot(t)
	contextDir := filepath.Join(root, "examples", "code-explainer", "fixtures")

	task := &models.TestCase{
		DisplayName: "Explain Python Recursion",
		Stimulus: models.TestStimulus{
			Message: "Explain this code to me",
			Metadata: map[string]any{
				"language":   "python",
				"complexity": "beginner",
			},
			Resources: []models.ResourceRef{
				{Location: "factorial.py"},
			},
		},
	}

	result := generateInstruction(task, contextDir, 10) // 10 KB threshold

	// Prompt
	require.Contains(t, result, "Explain this code to me")
	// Output location
	require.Contains(t, result, "/app/response.md")
	// Inlined file (factorial.py is <1KB, well under 10KB threshold)
	require.Contains(t, result, "### factorial.py")
	require.Contains(t, result, "```python")
	require.Contains(t, result, "def factorial(n):")
}

func TestGenerateInstruction_InlineBody(t *testing.T) {
	task := &models.TestCase{
		DisplayName: "Inline Body Test",
		Stimulus: models.TestStimulus{
			Message: "Review this code",
			Resources: []models.ResourceRef{
				{Location: "snippet.go", Body: "package main\nfunc main() {}"},
			},
		},
	}

	result := generateInstruction(task, "/nonexistent", 10)

	require.Contains(t, result, "### snippet.go")
	require.Contains(t, result, "```go")
	require.Contains(t, result, "package main")
}

func TestGenerateInstruction_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file larger than threshold (2KB threshold, file > 2KB)
	bigContent := strings.Repeat("x", 3*1024)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(bigContent), 0o644))

	task := &models.TestCase{
		DisplayName: "Large File Test",
		Stimulus: models.TestStimulus{
			Message:   "Process this file",
			Resources: []models.ResourceRef{{Location: "big.txt"}},
		},
	}

	result := generateInstruction(task, tmpDir, 2) // 2 KB threshold

	// File should be referenced, not inlined
	require.Contains(t, result, "`big.txt` → see `/app/fixtures/big.txt`")
	require.NotContains(t, result, bigContent)
}

func TestGenerateTaskTOML(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test-eval"},
		Config:       models.Config{TimeoutSec: 120},
	}
	task := &models.TestCase{
		TestID: "task-001",
		Tags:   []string{"python", "beginner"},
	}

	result := generateTaskTOML(spec, task)

	require.Contains(t, result, `version = "1.0"`)
	require.Contains(t, result, "[metadata]")
	require.Contains(t, result, `waza_eval = "test-eval"`)
	require.Contains(t, result, `waza_task_id = "task-001"`)
	// Tags should be quoted
	require.Contains(t, result, `"python"`)
	require.Contains(t, result, `"beginner"`)
	require.Contains(t, result, "[verifier]")
	require.Contains(t, result, "timeout_sec = 120")
	require.Contains(t, result, "[agent]")
	require.Contains(t, result, "[environment]")
}

func TestGenerateTaskTOML_DefaultTimeout(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "eval"},
		Config:       models.Config{TimeoutSec: 0},
	}
	task := &models.TestCase{TestID: "t1"}

	result := generateTaskTOML(spec, task)

	require.Contains(t, result, "timeout_sec = 300")
}

func TestGenerateDockerfile(t *testing.T) {
	result := generateDockerfile("ubuntu:22.04")

	require.Contains(t, result, "FROM ubuntu:22.04")
	require.Contains(t, result, "WORKDIR /app")
	require.Contains(t, result, "RUN apt-get update")
	require.Contains(t, result, "COPY waza /usr/local/bin/waza")
	require.Contains(t, result, "COPY waza-config/ /waza/")
}

func TestGenerateTestScript(t *testing.T) {
	result := generateTestScript("explain-python-001")

	require.Contains(t, result, "#!/bin/sh")
	require.Contains(t, result, "waza grade")
	require.Contains(t, result, `--task "explain-python-001"`)
	require.Contains(t, result, "--results")
	require.Contains(t, result, "waza-results.json")
	require.Contains(t, result, "reward.txt")
}

func TestExportTask_Integration(t *testing.T) {
	root := projectRoot(t)
	evalPath := filepath.Join(root, "examples", "code-explainer", "eval.yaml")
	specDir := filepath.Join(root, "examples", "code-explainer")

	spec, err := models.LoadBenchmarkSpec(evalPath)
	require.NoError(t, err)

	taskFiles, err := spec.ResolveTestFiles(specDir)
	require.NoError(t, err)
	require.NotEmpty(t, taskFiles)

	var tasks []*models.TestCase
	for _, f := range taskFiles {
		tc, err := models.LoadTestCase(f)
		require.NoError(t, err)
		tasks = append(tasks, tc)
	}

	outDir := t.TempDir()
	contextDir := filepath.Join(specDir, "fixtures")

	opts := &export.ExportOptions{
		Spec:             spec,
		OutputDir:        outDir,
		BaseImage:        "python:3.12-slim",
		FixtureThreshold: 10,
		ContextDir:       contextDir,
	}

	for _, task := range tasks {
		err := exportTask(opts, task)
		require.NoError(t, err)

		taskDir := filepath.Join(outDir, task.TestID)

		// instruction.md exists and has content
		instrData, err := os.ReadFile(filepath.Join(taskDir, "instruction.md"))
		require.NoError(t, err)
		require.Contains(t, string(instrData), task.Stimulus.Message)

		// task.toml exists
		_, err = os.Stat(filepath.Join(taskDir, "task.toml"))
		require.NoError(t, err)

		// Dockerfile exists
		dfData, err := os.ReadFile(filepath.Join(taskDir, "environment", "Dockerfile"))
		require.NoError(t, err)
		require.Contains(t, string(dfData), "FROM python:3.12-slim")

		// test.sh exists and is executable
		testShInfo, err := os.Stat(filepath.Join(taskDir, "tests", "test.sh"))
		require.NoError(t, err)
		require.NotZero(t, testShInfo.Mode()&0o100, "test.sh should be executable")

		// solution/ directory exists with solve.sh
		solveShPath := filepath.Join(taskDir, "solution", "solve.sh")
		solveShInfo, err := os.Stat(solveShPath)
		require.NoError(t, err)
		require.NotZero(t, solveShInfo.Mode()&0o100, "solve.sh should be executable")

		// waza-config/eval.yaml exists
		_, err = os.Stat(filepath.Join(taskDir, "environment", "waza-config", "eval.yaml"))
		require.NoError(t, err)

		// waza-config/task.yaml exists
		_, err = os.Stat(filepath.Join(taskDir, "environment", "waza-config", "task.yaml"))
		require.NoError(t, err)

		// fixtures directory has files
		fixturesDir := filepath.Join(taskDir, "environment", "waza-config", "fixtures")
		entries, err := os.ReadDir(fixturesDir)
		require.NoError(t, err)
		if len(task.Stimulus.Resources) > 0 && task.Stimulus.Resources[0].Location != "" {
			require.NotEmpty(t, entries, "fixtures directory should contain copied files")
		}
	}
}

func TestExportIncludesAgentAndREADME(t *testing.T) {
	root := projectRoot(t)
	evalPath := filepath.Join(root, "examples", "code-explainer", "eval.yaml")
	specDir := filepath.Join(root, "examples", "code-explainer")

	spec, err := models.LoadBenchmarkSpec(evalPath)
	require.NoError(t, err)

	taskFiles, err := spec.ResolveTestFiles(specDir)
	require.NoError(t, err)

	var tasks []*models.TestCase
	for _, f := range taskFiles {
		tc, err := models.LoadTestCase(f)
		require.NoError(t, err)
		tasks = append(tasks, tc)
	}

	outDir := t.TempDir()
	exporter := &Exporter{}
	err = exporter.Export(context.Background(), &export.ExportOptions{
		Spec:             spec,
		Tasks:            tasks,
		SpecDir:          specDir,
		OutputDir:        outDir,
		BaseImage:        "ubuntu:24.04",
		FixtureThreshold: 10,
		ContextDir:       filepath.Join(specDir, "fixtures"),
	})
	require.NoError(t, err)

	// Agent files should be present
	require.FileExists(t, filepath.Join(outDir, "agent", "waza_agent.py"))
	require.FileExists(t, filepath.Join(outDir, "agent", "install-waza.sh.j2"))

	// Agent file should contain WazaAgent class
	agentContent, err := os.ReadFile(filepath.Join(outDir, "agent", "waza_agent.py"))
	require.NoError(t, err)
	require.Contains(t, string(agentContent), "class WazaAgent")

	// README should be present with usage instructions
	readme, err := os.ReadFile(filepath.Join(outDir, "README.md"))
	require.NoError(t, err)
	require.Contains(t, string(readme), "agent.waza_agent:WazaAgent")
	require.Contains(t, string(readme), "COPILOT_GITHUB_TOKEN")
}

func TestMatchesGlob(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"*.py", "hello.py", true},
		{"*.py", "hello.js", false},
		{"*.PY", "hello.py", true}, // case insensitive
		{"test_*", "test_foo", true},
		{"test_*", "foo_test", false},
		{"*", "anything", true},
		{"?.go", "x.go", true},
		{"?.go", "xy.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.input, func(t *testing.T) {
			got := MatchesGlob(tt.pattern, tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDetectLang(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"factorial.py", "python"},
		{"app.js", "javascript"},
		{"main.go", "go"},
		{"query.sql", "sql"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"unknown.xyz", ""},
		{"Makefile", ""},
		{"code.ts", "typescript"},
		{"style.css", "css"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := detectLang(tt.path)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateSolveScript_WithRequiredStrings(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "check_keywords",
				Parameters: map[string]any{
					"contains":    []any{"hello", "world"},
					"contains_cs": []any{"Exact"},
				},
			},
			{
				Kind:       models.GraderKindInlineScript,
				Identifier: "code_check",
				Parameters: map[string]any{"assertions": []any{"len(output) > 5"}},
			},
		},
	}
	task := &models.TestCase{
		Expectation: models.TestExpectation{
			MustInclude: []string{"required"},
		},
	}

	result := generateSolveScript(spec, task)

	require.Contains(t, result, "#!/bin/sh")
	require.Contains(t, result, "cat > /app/response.md")
	require.Contains(t, result, "required")
	require.Contains(t, result, "hello")
	require.Contains(t, result, "world")
	require.Contains(t, result, "Exact")
	require.NotContains(t, result, "len(output)")
}

func TestGenerateSolveScript_NoRequiredStrings(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindInlineScript,
				Identifier: "code_check",
				Parameters: map[string]any{"assertions": []any{"len(output) > 5"}},
			},
		},
	}
	task := &models.TestCase{}

	result := generateSolveScript(spec, task)

	require.Contains(t, result, "#!/bin/sh")
	require.Contains(t, result, "stub solution")
	require.NotContains(t, result, "cat > /app/response.md")
}

func TestGenerateSolveScript_Deduplicates(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "g1",
				Parameters: map[string]any{"contains": []any{"hello"}},
			},
		},
	}
	task := &models.TestCase{
		Expectation: models.TestExpectation{
			MustInclude: []string{"hello"},
		},
	}

	result := generateSolveScript(spec, task)

	// "hello" should appear exactly once in the heredoc body
	body := strings.SplitAfter(result, "WAZA_SOLUTION_EOF\n")[0]
	require.Equal(t, 1, strings.Count(body, "hello"))
}
