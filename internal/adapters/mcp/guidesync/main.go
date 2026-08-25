package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	sourceDir := "guide_assets"
	destDir := "../../../.github/skills/rhizome-task-workflow/references"

	if err := os.MkdirAll(destDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s: %v\n", destDir, err)
		os.Exit(1)
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading directory %s: %v\n", sourceDir, err)
		os.Exit(1)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}

		sourcePath := filepath.Join(sourceDir, entry.Name())
		destPath := filepath.Join(destDir, entry.Name())

		src, err := os.Open(sourcePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", sourcePath, err)
			os.Exit(1)
		}
		defer src.Close()

		dst, err := os.Create(destPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating %s: %v\n", destPath, err)
			os.Exit(1)
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			fmt.Fprintf(os.Stderr, "Error copying %s to %s: %v\n", sourcePath, destPath, err)
			os.Exit(1)
		}

		fmt.Printf("Wrote %s\n", destPath)
	}
}
