//go:build windows

package plugins

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Create suspended, assign ownership, then resume: the plugin cannot spawn a
// descendant before joining the kill-on-close job. exec.Cmd closes the primary
// thread handle internally, so reopen that still-suspended thread by its PID.
func startManagedProcess(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, errors.New("cannot create plugin process job")
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, errors.New("cannot configure plugin process job")
	}
	var once sync.Once
	var startup sync.Mutex
	startup.Lock()
	finishStartup := sync.OnceFunc(startup.Unlock)
	// Cancellation cannot close/recycle the job handle while startup is using
	// it. A cancelled process is still attached before it can execute code.
	cleanup := func() {
		startup.Lock()
		defer startup.Unlock()
		once.Do(func() { _ = windows.CloseHandle(job) })
	}
	setProcAttr(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.Cancel = func() error { cleanup(); return cmd.Process.Kill() }
	if err := cmd.Start(); err != nil {
		finishStartup()
		cleanup()
		return nil, err
	}
	abort := func(message string) (func(), error) {
		finishStartup()
		cleanup()
		_ = cmd.Process.Kill()
		// No parser/stderr reader has started yet. Unblock exec copying before
		// joining it, even if process startup failed after resuming the thread.
		for _, writer := range []io.Writer{cmd.Stdout, cmd.Stderr} {
			if closer, ok := writer.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		_ = cmd.Wait()
		return nil, errors.New(message)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return abort("cannot open suspended plugin process")
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		return abort("cannot attach suspended plugin process to job")
	}
	thread, err := suspendedPluginThread(uint32(cmd.Process.Pid))
	if err != nil {
		return abort("cannot identify suspended plugin thread")
	}
	count, err := windows.ResumeThread(thread)
	windows.CloseHandle(thread)
	if err != nil || count != 1 {
		return abort("cannot resume plugin process")
	}
	finishStartup()
	return cleanup, nil
}

func suspendedPluginThread(pid uint32) (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	var threadID uint32
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		// A newly created suspended process has its initial thread. If an
		// external injector added another, fail closed rather than guessing.
		if threadID != 0 {
			return 0, errors.New("ambiguous initial plugin thread")
		}
		threadID = entry.ThreadID
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, err
	}
	if threadID == 0 {
		return 0, errors.New("initial plugin thread not found")
	}
	return windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
}
