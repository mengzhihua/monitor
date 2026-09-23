//go:build linux

package collect

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// attachTraceKprobes installs the program-family probes Netdata's ebpf.plugin
// counts, through tracefs. Missing permission or an absent tracefs leaves the
// procfs approximation in place.
func attachTraceKprobes() {
	f, err := os.OpenFile("/sys/kernel/debug/tracing/kprobe_events", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		tryBPFSyscall()
		return
	}
	defer f.Close()
	for _, ev := range []string{
		"p:monitor_add_to_page_cache_lru add_to_page_cache_lru",
		"p:monitor_mark_page_accessed mark_page_accessed",
		"p:monitor_d_lookup d_lookup",
		"p:monitor_vfs_read vfs_read",
		"p:monitor_vfs_write vfs_write",
		"p:monitor_oom_kill_process oom_kill_process",
		"p:monitor_kernel_clone kernel_clone",
		"p:monitor_shmget shmget",
		"p:monitor_swap_readpage swap_readpage",
		"p:monitor_sys_sync sys_sync",
		"p:monitor_md_flush_request md_flush_request",
		"p:monitor_attach_recursive_mnt attach_recursive_mnt",
		"p:monitor_handle_irq_event handle_irq_event_percpu",
		"p:monitor_blk_account_io_start blk_account_io_start",
		"p:monitor_do_sys_openat2 do_sys_openat2",
	} {
		_, _ = f.WriteString(ev + "\n")
	}
	tryBPFSyscall()
}

// tryBPFSyscall loads a two-instruction socket filter through bpf(2).
// Success means the kernel accepted a BPF program from this process. Full
// CO-RE relocation still needs a BTF-bearing object and /sys/kernel/btf/vmlinux;
// that file is loaded by loadBPFObject when collectors.modules.ebpf.core_object is set.
func tryBPFSyscall() bool {
	type insn struct {
		code uint16
		regs uint8
		off  int16
		imm  int32
	}
	prog := []insn{
		{code: 0xb7, regs: 0, imm: 0}, // mov r0, 0
		{code: 0x95},                  // exit
	}
	lic := []byte("GPL\x00")
	attr := struct {
		progType    uint32
		insnCnt     uint32
		insns       uint64
		license     uint64
		logLevel    uint32
		logSize     uint32
		logBuf      uint64
		kernVersion uint32
	}{
		progType: 1, // BPF_PROG_TYPE_SOCKET_FILTER
		insnCnt:  uint32(len(prog)),
		insns:    uint64(uintptr(unsafe.Pointer(&prog[0]))),
		license:  uint64(uintptr(unsafe.Pointer(&lic[0]))),
	}
	r1, _, errno := unix.Syscall(unix.SYS_BPF, uintptr(unix.BPF_PROG_LOAD), uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
	if errno != 0 {
		return false
	}
	unix.Close(int(r1))
	return true
}
