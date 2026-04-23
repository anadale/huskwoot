package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// promptOverrides содержит пользовательские шаблоны промптов, загруженные из файлов.
// Пустая строка означает использование встроенного шаблона.
type promptOverrides struct {
	groupClassifierSystem  string
	simpleClassifierSystem string
	extractorSystem        string
	extractorUser          string
	agentSystem            string
}

// loadPromptOverrides загружает все переопределения шаблонов из configDir/prompts/.
func loadPromptOverrides(configDir string) (promptOverrides, error) {
	var o promptOverrides
	names := []struct {
		name string
		dst  *string
	}{
		{"group-classifier-system", &o.groupClassifierSystem},
		{"simple-classifier-system", &o.simpleClassifierSystem},
		{"extractor-system", &o.extractorSystem},
		{"extractor-user", &o.extractorUser},
		{"agent-system", &o.agentSystem},
	}
	for _, n := range names {
		content, err := loadPrompt(configDir, n.name)
		if err != nil {
			return promptOverrides{}, err
		}
		*n.dst = content
	}
	return o, nil
}

// loadPrompt читает файл шаблона из configDir/prompts/<name>.gotmpl.
// Возвращает содержимое файла или пустую строку, если файл не найден.
func loadPrompt(configDir, name string) (string, error) {
	path := filepath.Join(configDir, "prompts", name+".gotmpl")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading template %q: %w", name, err)
	}
	return string(data), nil
}
