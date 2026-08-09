package main

import (
	"archive/zip"
	"embed"
	"io"
	"os"
)

//go:embed *.go go.mod go.sum README.md LICENSE docs/PROJECT.md
var embeddedSource embed.FS

func sourceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		writeTextf(stderr, "usage: %s\n", sourceUsage)
		return 2
	}
	if err := writeSourceArchive(args[0]); err != nil {
		writeTextf(stderr, "ssh-handoff source: %v\n", err)
		return 1
	}
	writeTextf(stdout, "wrote embedded source to %s\n", args[0])
	return 0
}

func writeSourceArchive(outputPath string) (err error) {
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(outputPath)
		}
	}()

	archive := zip.NewWriter(output)
	if err := archive.AddFS(embeddedSource); err != nil {
		return err
	}
	return archive.Close()
}
