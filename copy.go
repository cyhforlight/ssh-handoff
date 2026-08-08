package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func copyCommand(registry *sessionRegistry, args []string, stdout, stderr io.Writer) int {
	if len(args) != 4 || (args[0] != "upload" && args[0] != "download") {
		writeTextf(stderr, "usage: %s\n", copyUsage)
		return 2
	}

	session, err := registry.resolve(args[1])
	if err != nil {
		writeTextf(stderr, "ssh-handoff copy: %v\n", err)
		return 1
	}
	if session.Mode != modeExec {
		writeTextf(stderr, "ssh-handoff copy: session %s requires exec mode\n", session.ID)
		return 1
	}

	if args[0] == "upload" {
		err = uploadFile(session, args[2], args[3], stderr)
	} else {
		err = downloadFile(session, args[2], args[3], stderr)
	}
	if err != nil {
		writeTextf(stderr, "ssh-handoff copy: %v\n", err)
		return 1
	}
	writeTextf(stdout, "copied %s via %s\n", args[0], session.ID)
	return 0
}

func uploadFile(session *session, localPath, remotePath string, stderr io.Writer) error {
	source, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	command := "dst=" + quotePOSIXShell(remotePath) +
		`; tmp="${dst}.ssh-handoff-$$"` +
		`; trap 'rm -f -- "$tmp"' 0 HUP INT TERM` +
		`; [ ! -d "$dst" ] && cat > "$tmp" && mv -f -- "$tmp" "$dst"`
	return runTransfer(session, command, source, io.Discard, stderr)
}

func downloadFile(session *session, remotePath, localPath string, stderr io.Writer) error {
	temporary, err := os.CreateTemp(filepath.Dir(localPath), ".ssh-handoff-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if err := runTransfer(session, "cat < "+quotePOSIXShell(remotePath), nil, temporary, stderr); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, localPath)
}

func runTransfer(session *session, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd, cleanup, err := newSessionCommand(context.Background(), session, command)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

func quotePOSIXShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
