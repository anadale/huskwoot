package ai

import "embed"

var ExportedLoadPrompt = loadPrompt
var ExportedPromptsFS embed.FS

func init() {
	ExportedPromptsFS = promptsFS
}
