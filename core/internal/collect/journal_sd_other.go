//go:build !linux || android

package collect

func startSDJournal(*logsCollector) bool { return false }
