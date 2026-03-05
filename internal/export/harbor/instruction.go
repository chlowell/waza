package harbor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// langFromExt maps file extensions to language identifiers for code fences.
var langFromExt = map[string]string{
	".py":    "python",
	".js":    "javascript",
	".ts":    "typescript",
	".go":    "go",
	".java":  "java",
	".rb":    "ruby",
	".rs":    "rust",
	".sql":   "sql",
	".sh":    "bash",
	".bash":  "bash",
	".css":   "css",
	".html":  "html",
	".xml":   "xml",
	".json":  "json",
	".yaml":  "yaml",
	".yml":   "yaml",
	".toml":  "toml",
	".md":    "markdown",
	".c":     "c",
	".cpp":   "cpp",
	".cs":    "csharp",
	".php":   "php",
	".r":     "r",
	".swift": "swift",
	".kt":    "kotlin",
}

// generateInstruction creates instruction.md content for a task.
func generateInstruction(task *models.TestCase, contextDir string, thresholdKB int) string {
	var b strings.Builder

	// Prompt body
	b.WriteString("Write your response to this message in `/app/response.md`: " + task.Stimulus.Message)
	b.WriteString("\n")

	// Files section
	if len(task.Stimulus.Resources) > 0 {
		b.WriteString("Reference the following files:")
		b.WriteString("\n## Files\n\n")
		for _, ref := range task.Stimulus.Resources {
			if ref.Body != "" {
				// Inline content from the resource ref itself
				lang := detectLang(ref.Location)
				fmt.Fprintf(&b, "### %s\n\n", filepath.Base(ref.Location))
				fmt.Fprintf(&b, "```%s\n%s\n```\n\n", lang, ref.Body)
				continue
			}
			if ref.Location == "" {
				continue
			}
			filename := filepath.Base(ref.Location)
			fullPath := filepath.Join(contextDir, ref.Location)
			info, err := os.Stat(fullPath)
			if err != nil {
				// File not accessible; reference it by path
				fmt.Fprintf(&b, "- `%s` → see `/app/fixtures/%s`\n", filename, filename)
				continue
			}
			sizeKB := int(info.Size() / 1024)
			if sizeKB < thresholdKB {
				content, err := os.ReadFile(fullPath)
				if err == nil {
					lang := detectLang(ref.Location)
					fmt.Fprintf(&b, "### %s\n\n", filename)
					fmt.Fprintf(&b, "```%s\n%s\n```\n\n", lang, strings.TrimRight(string(content), "\n"))
					continue
				}
			}
			fmt.Fprintf(&b, "- `%s` → see `/app/fixtures/%s`\n", filename, filename)
		}
	}

	return b.String()
}

// detectLang returns a language identifier for code fences based on file extension.
func detectLang(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := langFromExt[ext]; ok {
		return lang
	}
	return ""
}
