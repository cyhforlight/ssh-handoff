package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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

func uploadFile(session *session, localPath, remotePath string, stderr io.Writer) (err error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("upload source is not a regular file: %s", localPath)
	}
	source, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	temporary := quotePOSIXShell(remotePath + ".ssh-handoff-" + rand.Text()[:12])
	destination := quotePOSIXShell(remotePath)
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = runTransfer(ctx, session, "rm -f -- "+temporary, nil, io.Discard, io.Discard)
		}
	}()

	command := "[ ! -d " + destination + " ] && cat > " + temporary
	if err := runTransfer(context.Background(), session, command, source, io.Discard, stderr); err != nil {
		return err
	}
	// SSH can turn a local stdin read error into a normal remote EOF. Publish
	// only after our local read and the staging command have both succeeded.
	command = "[ ! -d " + destination + " ] && mv -f -- " + temporary + " " + destination
	return runTransfer(context.Background(), session, command, nil, io.Discard, stderr)
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

	source := quotePOSIXShell(remotePath)
	command := "[ -f " + source + " ] && cat < " + source
	if err := runTransfer(context.Background(), session, command, nil, temporary, stderr); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, localPath)
}

// os/exec special-cases *os.File by passing the fd directly to the child, bypassing Go's I/O layer and hiding local errors; wrapping forces a goroutine pipe.
type readerOnly struct{ io.Reader }
type writerOnly struct{ io.Writer }

func runTransfer(ctx context.Context, session *session, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd, cleanup, err := newSessionCommand(ctx, session, command)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	// Keep file I/O in Go: passing *os.File directly lets the SSH client hide
	// read/write failures behind the remote command's successful exit status.
	if stdin != nil {
		cmd.Stdin = readerOnly{stdin}
	}
	cmd.Stdout, cmd.Stderr = writerOnly{stdout}, writerOnly{stderr}
	return cmd.Run()
}

func quotePOSIXShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
