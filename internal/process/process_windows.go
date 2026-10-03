package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const createSuspended = 0x00000004
const jobObjectExtendedLimitInformation = 9
const jobObjectLimitKillOnJobClose = 0x00002000
const processJobAccess = 0x00000100 | 0x00000001

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	createJobObject      = kernel32.NewProc("CreateJobObjectW")
	setJobInformation    = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJob   = kernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject   = kernel32.NewProc("TerminateJobObject")
	resumeThread         = kernel32.NewProc("ResumeThread")
	createThreadSnapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	threadFirst          = kernel32.NewProc("Thread32First")
	threadNext           = kernel32.NewProc("Thread32Next")
	openThread           = kernel32.NewProc("OpenThread")
)

type jobBasicLimits struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIOCounters struct{ ReadOperations, WriteOperations, OtherOperations, ReadBytes, WriteBytes, OtherBytes uint64 }
type jobExtendedLimits struct {
	Basic                                                                        jobBasicLimits
	IO                                                                           jobIOCounters
	ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed uintptr
}

func configureProcess(cmd *exec.Cmd) (func() error, func(), error) {
	value, _, err := createJobObject.Call(0, 0)
	if value == 0 {
		return nil, nil, fmt.Errorf("create child job: %w", err)
	}
	job := syscall.Handle(value)
	closeJob := func() { _ = syscall.CloseHandle(job) }
	info := jobExtendedLimits{Basic: jobBasicLimits{LimitFlags: jobObjectLimitKillOnJobClose}}
	ok, _, err := setJobInformation.Call(uintptr(job), jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		closeJob()
		return nil, nil, fmt.Errorf("configure child job: %w", err)
	}
	// Suspension closes the spawn/assignment race: no repository child can run
	// or create descendants until it belongs to the owned kill-on-close job.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		_, _, _ = terminateJobObject.Call(uintptr(job), 1)
		// Also kill an unassigned, suspended process if cancellation won the race
		// between Start and job assignment.
		return cmd.Process.Kill()
	}
	activate := func() error {
		handle, err := syscall.OpenProcess(processJobAccess, false, uint32(cmd.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended child: %w", err)
		}
		defer syscall.CloseHandle(handle)
		ok, _, err := assignProcessToJob.Call(uintptr(job), uintptr(handle))
		if ok == 0 {
			return fmt.Errorf("assign child job: %w", err)
		}
		return resumeInitialThread(uint32(cmd.Process.Pid))
	}
	return activate, closeJob, nil
}

// THREADENTRY32 is the documented Toolhelp thread snapshot record. A newly
// created suspended child has exactly one thread, whose ID os/exec discards.
type threadEntry struct {
	Size, Usage, ID, Owner      uint32
	BasePriority, DeltaPriority int32
	Flags                       uint32
}

func resumeInitialThread(pid uint32) error {
	value, _, err := createThreadSnapshot.Call(4, 0)
	if value == ^uintptr(0) {
		return fmt.Errorf("snapshot child thread: %w", err)
	}
	snapshot := syscall.Handle(value)
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, err := threadFirst.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.Owner == pid {
			thread, _, err := openThread.Call(2, 0, uintptr(entry.ID))
			if thread == 0 {
				return fmt.Errorf("open child thread: %w", err)
			}
			count, _, err := resumeThread.Call(thread)
			_ = syscall.CloseHandle(syscall.Handle(thread))
			if uint32(count) == ^uint32(0) {
				return fmt.Errorf("resume child thread: %w", err)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, err = threadNext.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("find suspended child thread: %w", err)
}
