//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPTY struct {
	Input  *os.File
	Output *os.File

	console   windows.Handle
	process   windows.Handle
	job       windows.Handle
	pid       int
	consoleMu sync.Mutex
}

func startWindowsPTY(path string, arguments []string, width, height int) (*windowsPTY, error) {
	size := windows.Coord{X: terminalDimension(width), Y: terminalDimension(height)}

	var inputRead windows.Handle
	var inputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("create ConPTY input pipe: %w", err)
	}
	inputReadOpen := true
	inputWriteOpen := true
	defer func() {
		if inputReadOpen {
			_ = windows.CloseHandle(inputRead)
		}
		if inputWriteOpen {
			_ = windows.CloseHandle(inputWrite)
		}
	}()

	var outputRead windows.Handle
	var outputWrite windows.Handle
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("create ConPTY output pipe: %w", err)
	}
	outputReadOpen := true
	outputWriteOpen := true
	defer func() {
		if outputReadOpen {
			_ = windows.CloseHandle(outputRead)
		}
		if outputWriteOpen {
			_ = windows.CloseHandle(outputWrite)
		}
	}()

	var console windows.Handle
	if err := windows.CreatePseudoConsole(size, inputRead, outputWrite, 0, &console); err != nil {
		return nil, fmt.Errorf("create ConPTY: %w", err)
	}
	consoleOpen := true
	defer func() {
		if consoleOpen {
			if outputReadOpen {
				_ = windows.CloseHandle(outputRead)
				outputReadOpen = false
			}
			windows.ClosePseudoConsole(console)
		}
	}()

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, fmt.Errorf("create ConPTY process attributes: %w", err)
	}
	defer attributes.Delete()
	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE takes the HPCON handle value itself
	// as lpValue, unlike attributes whose value is stored behind a pointer.
	// Reinterpreting the handle bits avoids passing &console, which silently
	// leaves the child attached to the parent's standard handles.
	consoleValue := *(*unsafe.Pointer)(unsafe.Pointer(&console))
	if err := attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		consoleValue,
		unsafe.Sizeof(console),
	); err != nil {
		return nil, fmt.Errorf("attach ConPTY process attribute: %w", err)
	}

	job, err := createWindowsPTYJob()
	if err != nil {
		return nil, err
	}
	jobOpen := true
	defer func() {
		if jobOpen {
			_ = windows.CloseHandle(job)
		}
	}()

	commandLine, err := windows.UTF16PtrFromString(
		windows.ComposeCommandLine(append([]string{path}, arguments...)),
	)
	if err != nil {
		return nil, fmt.Errorf("build Plink command line: %w", err)
	}
	application, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("build Plink application path: %w", err)
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			// Explicitly clear inherited console handles. Without this flag,
			// console children launched under a console host or debugger can
			// bypass ConPTY for their ordinary stdout and stderr.
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	var process windows.ProcessInformation
	flags := uint32(
		windows.EXTENDED_STARTUPINFO_PRESENT |
			windows.CREATE_UNICODE_ENVIRONMENT |
			windows.CREATE_SUSPENDED,
	)
	if err := windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		false,
		flags,
		nil,
		nil,
		&startup.StartupInfo,
		&process,
	); err != nil {
		return nil, fmt.Errorf("start Plink in ConPTY: %w", err)
	}
	processOpen := true
	threadOpen := true
	defer func() {
		if processOpen {
			_ = windows.TerminateProcess(process.Process, 1)
			_, _ = windows.WaitForSingleObject(process.Process, 1000)
			_ = windows.CloseHandle(process.Process)
		}
		if threadOpen {
			_ = windows.CloseHandle(process.Thread)
		}
	}()

	if err := windows.AssignProcessToJobObject(job, process.Process); err != nil {
		return nil, fmt.Errorf("assign Plink to cleanup job: %w", err)
	}
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		return nil, fmt.Errorf("resume Plink process: %w", err)
	}
	_ = windows.CloseHandle(process.Thread)
	threadOpen = false

	// ConPTY owns its copies after CreateProcess. Closing ours lets pipe users
	// observe EOF when the pseudoconsole shuts down.
	_ = windows.CloseHandle(inputRead)
	inputReadOpen = false
	_ = windows.CloseHandle(outputWrite)
	outputWriteOpen = false

	terminal := &windowsPTY{
		Input:   os.NewFile(uintptr(inputWrite), "conpty-input"),
		Output:  os.NewFile(uintptr(outputRead), "conpty-output"),
		console: console,
		process: process.Process,
		job:     job,
		pid:     int(process.ProcessId),
	}
	inputWriteOpen = false
	outputReadOpen = false
	consoleOpen = false
	processOpen = false
	jobOpen = false
	return terminal, nil
}

func createWindowsPTYJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create Plink cleanup job: %w", err)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configure Plink cleanup job: %w", err)
	}
	return job, nil
}

func terminalDimension(value int) int16 {
	if value < 1 {
		return 1
	}
	if value > 32767 {
		return 32767
	}
	return int16(value)
}

func (terminal *windowsPTY) PID() int {
	return terminal.pid
}

func (terminal *windowsPTY) Resize(width, height int) error {
	terminal.consoleMu.Lock()
	defer terminal.consoleMu.Unlock()
	if terminal.console == 0 {
		return errors.New("ConPTY is closed")
	}
	return windows.ResizePseudoConsole(
		terminal.console,
		windows.Coord{X: terminalDimension(width), Y: terminalDimension(height)},
	)
}

func (terminal *windowsPTY) Wait() (int, error) {
	defer terminal.closeConsole()

	result, err := windows.WaitForSingleObject(terminal.process, windows.INFINITE)
	if err != nil {
		return 0, err
	}
	if result != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("wait for Plink returned status %d", result)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(terminal.process, &exitCode); err != nil {
		return 0, err
	}
	return int(exitCode), nil
}

func (terminal *windowsPTY) Terminate() {
	_ = windows.TerminateJobObject(terminal.job, 1)
}

func (terminal *windowsPTY) Close() {
	terminal.closeConsole()
	if terminal.Input != nil {
		_ = terminal.Input.Close()
	}
	if terminal.Output != nil {
		_ = terminal.Output.Close()
	}
	if terminal.process != 0 {
		_ = windows.CloseHandle(terminal.process)
		terminal.process = 0
	}
	if terminal.job != 0 {
		_ = windows.CloseHandle(terminal.job)
		terminal.job = 0
	}
}

func (terminal *windowsPTY) closeConsole() {
	terminal.consoleMu.Lock()
	defer terminal.consoleMu.Unlock()
	if terminal.console != 0 {
		windows.ClosePseudoConsole(terminal.console)
		terminal.console = 0
	}
}
