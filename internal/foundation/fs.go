package foundation

import (
	"fmt"
	"os"
)

var requiredDirs = []string{
	"config",
	"config/prompts",
	"config/dict",
	"preset",
	"data",
	"data/sources",
	"data/sources/original",
	"data/sources/html",
	"data/sources/markdown",
	"data/searchindex",
	"data/traces",
	"data/exports",
	"logs",
}

func EnsureDirectories(baseDir string) error {
	for _, dir := range requiredDirs {
		path := baseDir + "/" + dir
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("foundation: create dir %s: %w", path, err)
		}
	}
	return nil
}
