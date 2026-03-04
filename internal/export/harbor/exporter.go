package harbor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/export"
	"github.com/microsoft/waza/internal/models"
	"gopkg.in/yaml.v3"
)

// MatchesGlob checks if a string matches a simple glob pattern (* wildcards only).
func MatchesGlob(pattern, s string) bool {
	pattern = strings.ToLower(pattern)
	s = strings.ToLower(s)
	matched, _ := filepath.Match(pattern, s)
	return matched
}

// Exporter implements export.Exporter for the Harbor format.
type Exporter struct{}

var _ export.Exporter = (*Exporter)(nil)

func (e *Exporter) Format() string { return "harbor" }

// Export writes Harbor task directories for each TestCase.
func (e *Exporter) Export(_ context.Context, opts *export.ExportOptions) error {
	for _, task := range opts.Tasks {
		if err := exportTask(opts, task); err != nil {
			return fmt.Errorf("exporting task %s: %w", task.TestID, err)
		}
	}
	return nil
}

func exportTask(opts *export.ExportOptions, task *models.TestCase) error {
	taskDir := filepath.Join(opts.OutputDir, task.TestID)
	envDir := filepath.Join(taskDir, "environment")
	wazaCfgDir := filepath.Join(envDir, "waza-config")
	fixturesDir := filepath.Join(wazaCfgDir, "fixtures")
	testsDir := filepath.Join(taskDir, "tests")
	solutionDir := filepath.Join(taskDir, "solution")

	for _, dir := range []string{taskDir, envDir, wazaCfgDir, fixturesDir, testsDir, solutionDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	contextDir := opts.ContextDir
	if contextDir == "" {
		contextDir = opts.SpecDir
	}

	// 1. instruction.md
	instruction := generateInstruction(task, contextDir, opts.FixtureThreshold)
	if err := os.WriteFile(filepath.Join(taskDir, "instruction.md"), []byte(instruction), 0o644); err != nil {
		return err
	}

	// 2. task.toml
	toml := generateTaskTOML(opts.Spec, task)
	if err := os.WriteFile(filepath.Join(taskDir, "task.toml"), []byte(toml), 0o644); err != nil {
		return err
	}

	// 3. Dockerfile
	dockerfile := generateDockerfile(opts.BaseImage)
	if err := os.WriteFile(filepath.Join(envDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}

	// 4. test.sh
	testScript := generateTestScript(task.TestID)
	if err := os.WriteFile(filepath.Join(testsDir, "test.sh"), []byte(testScript), 0o755); err != nil {
		return err
	}

	// 5. Copy fixtures
	if err := copyFixtures(task, contextDir, fixturesDir); err != nil {
		return fmt.Errorf("copying fixtures: %w", err)
	}

	// 6. Minimal eval.yaml
	if err := writeMinimalEvalYAML(opts.Spec, task, wazaCfgDir); err != nil {
		return fmt.Errorf("writing eval.yaml: %w", err)
	}

	// 7. task.yaml
	if err := writeTaskYAML(task, wazaCfgDir); err != nil {
		return fmt.Errorf("writing task.yaml: %w", err)
	}

	return nil
}

// copyFixtures copies resource files into the fixtures directory.
func copyFixtures(task *models.TestCase, contextDir, fixturesDir string) error {
	for _, ref := range task.Stimulus.Resources {
		if ref.Location == "" {
			continue
		}
		src := filepath.Join(contextDir, ref.Location)
		dst := filepath.Join(fixturesDir, filepath.Base(ref.Location))
		if err := copyFile(src, dst); err != nil {
			// If file doesn't exist, write inline body if available
			if os.IsNotExist(err) && ref.Body != "" {
				if writeErr := os.WriteFile(dst, []byte(ref.Body), 0o644); writeErr != nil {
					return writeErr
				}
				continue
			}
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	//nolint:errcheck
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	//nolint:errcheck
	defer out.Close()

	if _, err = io.Copy(out, in); err == nil {
		err = out.Close()
	}
	return err
}

// minimalSpec is the structure written to the Harbor waza-config eval.yaml.
type minimalSpec struct {
	Name    string                `yaml:"name"`
	Config  minimalConfig         `yaml:"config"`
	Graders []models.GraderConfig `yaml:"graders"`
	Tasks   []string              `yaml:"tasks"`
}

type minimalConfig struct {
	TrialsPerTask  int `yaml:"trials_per_task"`
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

func writeMinimalEvalYAML(spec *models.BenchmarkSpec, task *models.TestCase, wazaCfgDir string) error {
	// Merge global graders with task-specific validators
	graders := make([]models.GraderConfig, len(spec.Graders))
	copy(graders, spec.Graders)
	for _, v := range task.Validators {
		graders = append(graders, models.GraderConfig{
			Kind:       v.Kind,
			Identifier: v.Identifier,
			Weight:     v.Weight,
			Rubric:     v.Rubric,
			Parameters: v.Parameters,
		})
	}

	timeout := spec.Config.TimeoutSec
	if timeout <= 0 {
		timeout = 300
	}

	ms := minimalSpec{
		Name: spec.Name,
		Config: minimalConfig{
			TrialsPerTask:  1,
			TimeoutSeconds: timeout,
		},
		Graders: graders,
		Tasks:   []string{"task.yaml"},
	}

	data, err := yaml.Marshal(&ms)
	if err != nil {
		return err
	}

	header := "# Minimal eval spec generated by waza export for Harbor\n"
	return os.WriteFile(filepath.Join(wazaCfgDir, "eval.yaml"), append([]byte(header), data...), 0o644)
}

func writeTaskYAML(task *models.TestCase, wazaCfgDir string) error {
	// Strip validators since they're merged into eval.yaml graders
	exported := *task
	exported.Validators = nil

	// Rewrite resource locations to reference the fixtures directory
	for i := range exported.Stimulus.Resources {
		if exported.Stimulus.Resources[i].Location != "" {
			exported.Stimulus.Resources[i].Location = filepath.Base(exported.Stimulus.Resources[i].Location)
		}
	}

	data, err := yaml.Marshal(&exported)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(wazaCfgDir, "task.yaml"), data, 0o644)
}

// generateDockerfile creates Dockerfile content for a Harbor task environment.
func generateDockerfile(baseImage string) string {
	var b strings.Builder

	b.WriteString("FROM ")
	b.WriteString(baseImage)
	b.WriteString("\n\n")
	b.WriteString("WORKDIR /app\n\n")
	b.WriteString("# Install dependencies for waza graders\n")
	b.WriteString("RUN apt-get update && \\\n")
	b.WriteString("    apt-get install -y ca-certificates python3 && \\\n")
	b.WriteString("    rm -rf /var/lib/apt/lists/*\n\n")
	b.WriteString("# Install waza binary\n")
	b.WriteString("COPY waza /usr/local/bin/waza\n\n")
	b.WriteString("# Copy waza configuration for grading\n")
	b.WriteString("COPY waza-config/ /waza/\n\n")
	b.WriteString("# Pre-load fixtures in the workspace\n")
	b.WriteString("COPY waza-config/fixtures/ /app/fixtures/\n")

	return b.String()
}

// generateTaskTOML creates task.toml content for a Harbor task.
func generateTaskTOML(spec *models.BenchmarkSpec, task *models.TestCase) string {
	//nolint:errcheck

	timeout := spec.Config.TimeoutSec
	if timeout <= 0 {
		timeout = 300
	}

	var b strings.Builder

	b.WriteString("version = \"1.0\"\n\n")

	b.WriteString("[metadata]\n")
	b.WriteString("source = \"waza\"\n")
	fmt.Fprintf(&b, "waza_eval = %q\n", spec.Name)
	fmt.Fprintf(&b, "waza_task_id = %q\n", task.TestID)
	if len(task.Tags) > 0 {
		b.WriteString("tags = [")
		for i, tag := range task.Tags {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", tag)
		}
		b.WriteString("]\n")
	} else {
		b.WriteString("tags = []\n")
	}

	b.WriteString("\n[verifier]\n")
	fmt.Fprintf(&b, "timeout_sec = %d\n", timeout)

	b.WriteString("\n[agent]\n")
	fmt.Fprintf(&b, "timeout_sec = %d\n", timeout)

	b.WriteString("\n[environment]\n")
	b.WriteString("build_timeout_sec = 600.0\n")
	b.WriteString("cpus = 1\n")
	b.WriteString("memory = \"2G\"\n")
	b.WriteString("storage = \"10G\"\n")

	return b.String()
}

// generateTestScript creates test.sh content for a Harbor task.
func generateTestScript(taskID string) string {
	//nolint:errcheck

	var b strings.Builder

	b.WriteString("#!/bin/bash\n\n")
	fmt.Fprintf(&b, "# Waza-Harbor bridge: run waza graders against agent output\n")
	fmt.Fprintf(&b, "# Task: %s\n\n", taskID)
	b.WriteString("RESPONSE_FILE=\"/app/response.md\"\n\n")
	b.WriteString("# Check if agent produced output\n")
	b.WriteString("if [ ! -f \"$RESPONSE_FILE\" ]; then\n")
	b.WriteString("    echo '{\"overall_score\": 0, \"error\": \"No response file found\"}' > /logs/verifier/reward.json\n")
	b.WriteString("    exit 0\n")
	b.WriteString("fi\n\n")
	b.WriteString("# Run waza graders\n")
	fmt.Fprintf(&b, "waza grade /waza/eval.yaml \\\n")
	fmt.Fprintf(&b, "    --task %q \\\n", taskID)
	b.WriteString("    --output \"$RESPONSE_FILE\" \\\n")
	b.WriteString("    --context-dir /waza/fixtures \\\n")
	b.WriteString("    --workspace /app \\\n")
	b.WriteString("    --reward-file /logs/verifier/reward.json \\\n")
	b.WriteString("    --reward-format json\n\n")
	b.WriteString("# Fallback: if waza grade failed, write zero reward\n")
	b.WriteString("if [ $? -ne 0 ]; then\n")
	b.WriteString("    echo 0 > /logs/verifier/reward.txt\n")
	b.WriteString("fi\n")

	return b.String()
}
