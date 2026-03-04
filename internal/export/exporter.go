package export

import (
	"context"

	"github.com/microsoft/waza/internal/models"
)

// Exporter converts a waza eval project into an external format.
type Exporter interface {
	// Format returns the name of the export format, e.g. "harbor".
	Format() string
	// Export performs the export operation, writing files to disk as needed.
	Export(context.Context, *ExportOptions) error
}

// ExportOptions holds the configuration for an export operation.
type ExportOptions struct {
	Spec             *models.BenchmarkSpec
	Tasks            []*models.TestCase
	SpecDir          string // Directory containing eval.yaml (for resolving fixtures)
	OutputDir        string // Where to write exported files
	BaseImage        string // Base Docker image
	FixtureThreshold int    // Max KB to inline fixtures
	ContextDir       string // Fixtures directory
}
