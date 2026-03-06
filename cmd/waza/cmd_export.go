package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/export"
	"github.com/microsoft/waza/internal/export/harbor"
	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
)

func newExportCommand() *cobra.Command {
	var (
		format           string
		outputDir        string
		taskFilters      []string
		tagFilters       []string
		baseImage        string
		fixtureThreshold int
	)

	cmd := &cobra.Command{
		Use:   "export <eval.yaml>",
		Short: "Export a waza eval project to an external format",
		Long: `Export a waza eval project into an external format for use with
third-party evaluation platforms.

Currently supported formats:
  harbor  - Harbor evaluation platform`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runExport(cmd, args[0], format, outputDir, taskFilters, tagFilters, baseImage, fixtureThreshold)
		},
		SilenceErrors: true,
	}

	cmd.Flags().StringVarP(&format, "format", "f", "", "Output format (required): harbor")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "o", "", "Output directory (default: ./<format>-tasks/)")
	cmd.Flags().StringSliceVar(&taskFilters, "task", nil, "Filter tasks by name/ID glob pattern")
	cmd.Flags().StringSliceVar(&tagFilters, "tags", nil, "Filter tasks by tags")
	cmd.Flags().StringVar(&baseImage, "base-image", "ubuntu:24.04", "Base Docker image")
	cmd.Flags().IntVar(&fixtureThreshold, "fixture-threshold", 10, "Max fixture size in KB to inline in instruction")

	return cmd
}

func runExport(cmd *cobra.Command, evalPath, format, outputDir string, taskFilters, tagFilters []string, baseImage string, fixtureThreshold int) error {
	if format == "" {
		return errors.New("--format is required")
	}

	// Resolve eval.yaml path
	absPath, err := filepath.Abs(evalPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	specDir := filepath.Dir(absPath)

	// Load spec
	spec, err := models.LoadBenchmarkSpec(absPath)
	if err != nil {
		return fmt.Errorf("loading spec: %w", err)
	}

	// Resolve and load tasks
	taskFiles, err := spec.ResolveTestFiles(specDir)
	if err != nil {
		return fmt.Errorf("resolving tasks: %w", err)
	}

	var tasks []*models.TestCase
	for _, tf := range taskFiles {
		tc, err := models.LoadTestCase(tf)
		if err != nil {
			return fmt.Errorf("loading task %s: %w", tf, err)
		}
		tasks = append(tasks, tc)
	}

	// Apply filters
	tasks = filterTasks(tasks, taskFilters, tagFilters)

	if len(tasks) == 0 {
		return errors.New("no tasks matched the specified filters")
	}

	// Default output directory
	if outputDir == "" {
		outputDir = fmt.Sprintf("./%s-tasks", format)
	}

	// Create exporter
	var exporter export.Exporter
	switch format {
	case "harbor":
		exporter = &harbor.Exporter{}
	default:
		return fmt.Errorf("unsupported format %q: supported formats are: harbor", format)
	}

	opts := &export.ExportOptions{
		Spec:             spec,
		Tasks:            tasks,
		SpecDir:          specDir,
		OutputDir:        outputDir,
		BaseImage:        baseImage,
		FixtureThreshold: fixtureThreshold,
		ContextDir:       filepath.Join(specDir, "fixtures"),
	}

	if err := exporter.Export(context.Background(), opts); err != nil {
		return fmt.Errorf("export failed: %w", err)
	}

	fmt.Printf("✓ Exported %d tasks to %s format in %s\n", len(tasks), exporter.Format(), outputDir)
	return nil
}

// filterTasks applies task name/ID glob and tag filters.
func filterTasks(tasks []*models.TestCase, taskFilters, tagFilters []string) []*models.TestCase {
	if len(taskFilters) == 0 && len(tagFilters) == 0 {
		return tasks
	}

	var result []*models.TestCase
	for _, task := range tasks {
		if matchesTaskFilter(task, taskFilters) && matchesTagFilter(task, tagFilters) {
			result = append(result, task)
		}
	}
	return result
}

func matchesTaskFilter(task *models.TestCase, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if harbor.MatchesGlob(p, task.TestID) || harbor.MatchesGlob(p, task.DisplayName) {
			return true
		}
	}
	return false
}

func matchesTagFilter(task *models.TestCase, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	for _, required := range tags {
		for _, taskTag := range task.Tags {
			if strings.EqualFold(required, taskTag) {
				return true
			}
		}
	}
	return false
}
