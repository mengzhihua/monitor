//go:build windows

package collect

import (
	"syscall"
	"unsafe"
)

// pdhFill reads a small English-counter set through pdh.dll. WMI remains the
// default live path; this runs only when collectors.modules.windows.pdh is set.
func pdhFill(dst perflibSnap) bool {
	if dst == nil {
		return false
	}
	pdh := syscall.NewLazyDLL("pdh.dll")
	openQuery := pdh.NewProc("PdhOpenQueryW")
	addCounter := pdh.NewProc("PdhAddEnglishCounterW")
	collect := pdh.NewProc("PdhCollectQueryData")
	getValue := pdh.NewProc("PdhGetFormattedCounterValue")
	closeQuery := pdh.NewProc("PdhCloseQuery")
	var query uintptr
	if r, _, _ := openQuery.Call(0, 0, uintptr(unsafe.Pointer(&query))); r != 0 || query == 0 {
		return false
	}
	defer closeQuery.Call(query)
	paths := []struct{ obj, inst, ctr, path string }{
		{"system", "", "processor queue length", `\System\Processor Queue Length`},
		{"memory", "", "available mbytes", `\Memory\Available MBytes`},
		{"memory", "", "committed bytes", `\Memory\Committed Bytes`},
		{"processor", "_total", "% processor time", `\Processor(_Total)\% Processor Time`},
	}
	type counter struct {
		obj, inst, ctr string
		h              uintptr
	}
	var cs []counter
	for _, p := range paths {
		path, err := syscall.UTF16PtrFromString(p.path)
		if err != nil {
			continue
		}
		var h uintptr
		if r, _, _ := addCounter.Call(query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&h))); r != 0 || h == 0 {
			continue
		}
		cs = append(cs, counter{p.obj, p.inst, p.ctr, h})
	}
	if len(cs) == 0 {
		return false
	}
	if r, _, _ := collect.Call(query); r != 0 {
		return false
	}
	ok := false
	for _, c := range cs {
		var formatted struct {
			status uint32
			_      uint32
			value  float64
		}
		var typ uint32
		// PDH_FMT_DOUBLE = 0x00000200. The value sits after CStatus in PDH_FMT_COUNTERVALUE.
		if r, _, _ := getValue.Call(c.h, 0x200, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&formatted))); r != 0 || formatted.status != 0 {
			continue
		}
		dst[perflibKey(c.obj, c.inst, c.ctr)] = formatted.value
		ok = true
	}
	return ok
}
