//go:build !linux

package collect

func readAuditNetlink() (auditStatus, bool) { return auditStatus{}, false }
