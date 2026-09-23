//go:build linux && !android

package collect

import (
	"context"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

func startSDJournal(l *logsCollector) bool {
	handle, err := purego.Dlopen("libsystemd.so.1", purego.RTLD_LAZY)
	if err != nil {
		handle, err = purego.Dlopen("libsystemd.so", purego.RTLD_LAZY)
		if err != nil {
			return false
		}
	}
	var (
		sdOpen    func(ret *uintptr, flags int32) int32
		sdClose   func(j uintptr)
		sdSeek    func(j uintptr) int32
		sdNext    func(j uintptr) int32
		sdGetData func(j uintptr, field string, data **byte, length *uintptr) int32
		sdWait    func(j uintptr, timeout uint64) int32
	)
	defer func() { _ = recover() }()
	purego.RegisterLibFunc(&sdOpen, handle, "sd_journal_open")
	purego.RegisterLibFunc(&sdClose, handle, "sd_journal_close")
	purego.RegisterLibFunc(&sdSeek, handle, "sd_journal_seek_tail")
	purego.RegisterLibFunc(&sdNext, handle, "sd_journal_next")
	purego.RegisterLibFunc(&sdGetData, handle, "sd_journal_get_data")
	purego.RegisterLibFunc(&sdWait, handle, "sd_journal_wait")

	var j uintptr
	if sdOpen(&j, 1) < 0 || j == 0 { // SD_JOURNAL_LOCAL_ONLY
		return false
	}
	if sdSeek(j) < 0 {
		sdClose(j)
		return false
	}
	_ = sdNext(j) // step onto the last record, then follow new ones
	field := func(name string) string {
		var data *byte
		var n uintptr
		if sdGetData(j, name, &data, &n) < 0 || data == nil || n == 0 {
			return ""
		}
		b := unsafe.Slice(data, int(n))
		s := string(b)
		if i := strings.IndexByte(s, '='); i >= 0 {
			s = s[i+1:]
		}
		return strings.TrimRight(s, "\x00")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.followOn = true
	go func() {
		defer cancel()
		defer sdClose(j)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if sdWait(j, 1_000_000) < 0 {
				return
			}
			for sdNext(j) > 0 {
				usec, _ := strconv.ParseInt(field("_SOURCE_REALTIME_TIMESTAMP"), 10, 64)
				row := LogRow{
					Time:     usec / 1000,
					Priority: field("PRIORITY"),
					Unit:     field("_SYSTEMD_UNIT"),
					PID:      field("_PID"),
					Message:  field("MESSAGE"),
					Cursor:   field("__CURSOR"),
					Boot:     field("_BOOT_ID"),
				}
				if row.Message == "" && row.Unit == "" {
					continue
				}
				l.mu.Lock()
				l.buf = append(l.buf, row)
				if len(l.buf) > 2000 {
					l.buf = l.buf[len(l.buf)-2000:]
				}
				l.pending++
				if l.pendingSev == nil {
					l.pendingSev = map[string]float64{}
				}
				sev := row.Priority
				if sev == "" {
					sev = "info"
				}
				l.pendingSev[sev]++
				l.mu.Unlock()
			}
		}
	}()
	return true
}
