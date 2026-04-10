package ai

import (
	"embed"
	"io/fs"
)

//go:embed prompts
var promptsFS embed.FS

// loadPrompt reads prompts/{name}_{lang}.tmpl from fsys.
// Falls back to "ru" if the requested language file is not found.
func loadPrompt(fsys embed.FS, lang, name string) string {
	path := "prompts/" + name + "_" + lang + ".tmpl"
	data, err := fs.ReadFile(fsys, path)
	if err == nil {
		return string(data)
	}
	path = "prompts/" + name + "_ru.tmpl"
	data, err = fs.ReadFile(fsys, path)
	if err != nil {
		return ""
	}
	return string(data)
}
