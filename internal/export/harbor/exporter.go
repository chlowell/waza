package harbor

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/export"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"gopkg.in/yaml.v3"
)

var (
	//go:embed Dockerfile_template
	dockerfileTemplate string
	//go:embed test.sh
	testScript string
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

const stagedSkillsContainerRoot = "/app/skills"

func (e *Exporter) Format() string { return "harbor" }

// Export writes Harbor task directories for each TestCase.
func (e *Exporter) Export(_ context.Context, opts *export.ExportOptions) error {
	for _, task := range opts.Tasks {
		if err := exportTask(opts, task); err != nil {
			return fmt.Errorf("exporting task %s: %w", task.TestID, err)
		}
	}

	// Copy waza agent files into the dataset
	if err := copyAgentFiles(opts.OutputDir); err != nil {
		return fmt.Errorf("copying agent files: %w", err)
	}

	// Generate README with usage instructions
	if err := writeDatasetREADME(opts); err != nil {
		return fmt.Errorf("writing README: %w", err)
	}

	return nil
}

func exportTask(opts *export.ExportOptions, task *models.TestCase) error {
	taskDir := filepath.Join(opts.OutputDir, task.TestID)
	envDir := filepath.Join(taskDir, "environment")
	wazaCfgDir := filepath.Join(envDir, "waza-config")
	fixturesDir := filepath.Join(wazaCfgDir, "fixtures")
	skillsDir := filepath.Join(envDir, "skills")
	testsDir := filepath.Join(taskDir, "tests")
	solutionDir := filepath.Join(taskDir, "solution")

	for _, dir := range []string{taskDir, envDir, wazaCfgDir, fixturesDir, skillsDir, testsDir, solutionDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	contextDir := opts.ContextDir
	if contextDir == "" {
		contextDir = opts.SpecDir
	}

	stagedSkills, err := collectStagedSkillDirs(opts)
	if err != nil {
		return fmt.Errorf("collecting skill directories: %w", err)
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

	// 5a. Copy the skill directories so Harbor agents can load the skill under test.
	if err := copyStagedSkillDirs(stagedSkills, skillsDir, opts.OutputDir); err != nil {
		return fmt.Errorf("copying skill directories: %w", err)
	}

	// 6. Minimal eval.yaml
	if err := writeMinimalEvalYAML(opts.Spec, task, wazaCfgDir, stagedSkills); err != nil {
		return fmt.Errorf("writing eval.yaml: %w", err)
	}

	// 7. task.yaml
	if err := writeTaskYAML(task, wazaCfgDir); err != nil {
		return fmt.Errorf("writing task.yaml: %w", err)
	}

	// 8. solution/solve.sh
	solveScript := generateSolveScript(opts.Spec, task)
	if err := os.WriteFile(filepath.Join(solutionDir, "solve.sh"), []byte(solveScript), 0o755); err != nil {
		return fmt.Errorf("writing solve.sh: %w", err)
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

type stagedSkillDir struct {
	SourcePath    string
	StagedDirName string
	ContainerPath string
}

func collectStagedSkillDirs(opts *export.ExportOptions) ([]stagedSkillDir, error) {
	var candidateDirs []string

	primarySkillDir, err := findSkillDir(resolveSkillSourceDir(opts))
	if err != nil {
		return nil, err
	}
	if primarySkillDir != "" {
		candidateDirs = append(candidateDirs, primarySkillDir)
	}

	for _, skillDir := range utils.ResolvePaths(opts.Spec.Config.SkillPaths, opts.SpecDir) {
		resolvedSkillDir, err := findSkillDir(skillDir)
		if err != nil {
			return nil, err
		}
		if resolvedSkillDir == "" {
			return nil, fmt.Errorf("configured skill directory %q does not contain SKILL.md", skillDir)
		}
		candidateDirs = append(candidateDirs, resolvedSkillDir)
	}

	seenPaths := map[string]bool{}
	seenNames := map[string]string{}
	staged := make([]stagedSkillDir, 0, len(candidateDirs))
	for _, dir := range candidateDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		if seenPaths[absDir] {
			continue
		}

		stagedName := filepath.Base(absDir)
		if prior, ok := seenNames[stagedName]; ok && prior != absDir {
			return nil, fmt.Errorf("skill directory basename conflict for %q: %q and %q", stagedName, prior, absDir)
		}

		seenPaths[absDir] = true
		seenNames[stagedName] = absDir
		staged = append(staged, stagedSkillDir{
			SourcePath:    absDir,
			StagedDirName: stagedName,
			ContainerPath: filepath.ToSlash(filepath.Join(stagedSkillsContainerRoot, stagedName)),
		})
	}

	return staged, nil
}

func copyStagedSkillDirs(stagedSkills []stagedSkillDir, skillsDir, outputDir string) error {
	for _, skillDir := range stagedSkills {
		dst := filepath.Join(skillsDir, skillDir.StagedDirName)
		if err := copyDir(skillDir.SourcePath, dst, outputDir); err != nil {
			return err
		}
	}
	return nil
}

func resolveSkillSourceDir(opts *export.ExportOptions) string {
	if opts.SpecDir != "" {
		return opts.SpecDir
	}
	if filepath.Base(opts.ContextDir) == "fixtures" {
		return filepath.Dir(opts.ContextDir)
	}
	return opts.ContextDir
}

func findSkillDir(specDir string) (string, error) {
	if specDir == "" {
		return "", nil
	}

	skillPath := filepath.Join(specDir, "SKILL.md")
	info, err := os.Stat(skillPath)
	if err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory, expected file", skillPath)
		}
		return specDir, nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", err
}

func copyDir(src, dst, excludeRoot string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		skip, err := shouldSkipCopyPath(srcPath, excludeRoot)
		if err != nil {
			return err
		}
		if skip {
			continue
		}

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath, excludeRoot); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}

	return nil
}

func shouldSkipCopyPath(path, excludeRoot string) (bool, error) {
	if excludeRoot == "" {
		return false, nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	absExcludeRoot, err := filepath.Abs(excludeRoot)
	if err != nil {
		return false, err
	}

	rel, err := filepath.Rel(absExcludeRoot, absPath)
	if err != nil {
		return false, err
	}

	return rel == "." || !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..", nil
}

// minimalSpec is the structure written to the Harbor waza-config eval.yaml.
type minimalSpec struct {
	Name    string                `yaml:"name"`
	Config  minimalConfig         `yaml:"config"`
	Graders []models.GraderConfig `yaml:"graders"`
	Tasks   []string              `yaml:"tasks"`
}

type minimalConfig struct {
	TrialsPerTask  int      `yaml:"trials_per_task"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	Executor       string   `yaml:"executor"`
	SkillPaths     []string `yaml:"skill_directories,omitempty"`
	RequiredSkills []string `yaml:"required_skills,omitempty"`
}

func writeMinimalEvalYAML(spec *models.BenchmarkSpec, task *models.TestCase, wazaCfgDir string, stagedSkills []stagedSkillDir) error {
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
			Executor:       "copilot-sdk",
			SkillPaths:     stagedSkillPaths(stagedSkills),
			RequiredSkills: append([]string(nil), spec.Config.RequiredSkills...),
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

func stagedSkillPaths(stagedSkills []stagedSkillDir) []string {
	if len(stagedSkills) == 0 {
		return nil
	}

	paths := make([]string, 0, len(stagedSkills))
	for _, skillDir := range stagedSkills {
		paths = append(paths, skillDir.ContainerPath)
	}
	return paths
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

func generateDockerfile(baseImage string) string {
	return strings.ReplaceAll(dockerfileTemplate, "{baseImage}", baseImage)
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
	b.WriteString("memory_mb = \"2048\"\n")
	b.WriteString("storage_mb = \"100\"\n")

	return b.String()
}

// generateTestScript creates test.sh content for a Harbor task.
func generateTestScript(taskID string) string {
	return strings.ReplaceAll(testScript, "{task}", taskID)
}

// generateSolveScript builds a solve.sh that writes synthetic output satisfying
// text graders and MustInclude expectations. Falls back to a stub if no
// required strings can be extracted from grader configs.
func generateSolveScript(spec *models.BenchmarkSpec, task *models.TestCase) string {
	seen := make(map[string]bool)
	var required []string
	add := func(s string) {
		lower := strings.ToLower(s)
		if !seen[lower] {
			seen[lower] = true
			required = append(required, s)
		}
	}

	// Collect from task expectations
	for _, s := range task.Expectation.MustInclude {
		add(s)
	}

	// Collect from text grader configs (global + task-specific)
	collectTextContains := func(params models.GraderParameters) {
		switch p := params.(type) {
		case models.TextGraderParameters:
			for _, s := range p.Contains {
				add(s)
			}
			for _, s := range p.ContainsCS {
				add(s)
			}
		case models.GenericGraderParameters:
			for _, key := range []string{"contains", "contains_cs"} {
				switch list := p[key].(type) {
				case []any:
					for _, item := range list {
						if s, ok := item.(string); ok {
							add(s)
						}
					}
				case []string:
					for _, s := range list {
						add(s)
					}
				}
			}
		}
	}

	for _, g := range spec.Graders {
		if g.Kind == models.GraderKindText {
			collectTextContains(g.Parameters)
		}
	}
	for _, v := range task.Validators {
		if v.Kind == models.GraderKindText {
			collectTextContains(v.Parameters)
		}
	}

	var b strings.Builder
	if len(required) == 0 {
		b.WriteString("#!/bin/sh\n\n")
		b.WriteString("# This is a stub solution script generated by waza export.\n")
		b.WriteString("echo 'TODO: implement solve.sh so the Harbor oracle can complete this task and satisfy its waza graders'\n")
		b.WriteString("exit 0\n")
	} else {
		b.WriteString("#!/bin/sh\n")
		b.WriteString("# This is a synthetic solution script generated by waza export to satisfy\n")
		b.WriteString("# simple text graders. It may need more logic to satisfy other graders.\n")
		b.WriteString("cat > /app/response.md << 'EOF'\n")
		b.WriteString(strings.Join(required, " "))
		b.WriteString("\nEOF\n")
	}
	return b.String()
}

//go:embed waza_agent.py
var wazaAgentPy string

//go:embed install-waza.sh.j2
var installWazaShJ2 string

// copyAgentFiles writes the waza Harbor agent into the dataset's agent/ directory.
func copyAgentFiles(outputDir string) error {
	agentDir := filepath.Join(outputDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(agentDir, "waza_agent.py"), []byte(wazaAgentPy), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(agentDir, "install-waza.sh.j2"), []byte(installWazaShJ2), 0o644); err != nil {
		return err
	}
	return nil
}

// writeDatasetREADME generates a README.md in the dataset root.
func writeDatasetREADME(opts *export.ExportOptions) error {
	//nolint:errcheck

	var b strings.Builder

	b.WriteString("# Harbor Dataset\n\n")
	fmt.Fprintf(&b, "Exported from waza eval: **%s**\n\n", opts.Spec.Name)
	fmt.Fprintf(&b, "Contains %d task(s).\n\n", len(opts.Tasks))

	b.WriteString("## Running with any Harbor agent\n\n")
	b.WriteString("```bash\n")
	b.WriteString("harbor run -p . -a <agent> -m <model>\n")
	b.WriteString("```\n\n")

	b.WriteString("## Running with the waza agent (Copilot SDK)\n\n")
	b.WriteString("The `agent/` directory contains a waza Harbor agent that uses the Copilot SDK.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("harbor run -p . \\\n")
	b.WriteString("    --agent-import-path agent.waza_agent:WazaAgent \\\n")
	b.WriteString("    -m copilot/gpt-4o\n")
	b.WriteString("```\n\n")
	b.WriteString("Set `COPILOT_GITHUB_TOKEN` in your environment for authentication.\n\n")
	b.WriteString("The waza agent collects Copilot session transcripts in `/logs/agent/transcripts/`.\n")

	return os.WriteFile(filepath.Join(opts.OutputDir, "README.md"), []byte(b.String()), 0o644)
}
