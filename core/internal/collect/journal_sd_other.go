//go:build !linux

package collect

func startSDJournal(*logsCollector) bool { return false }
