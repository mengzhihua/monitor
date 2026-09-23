//go:build !linux

package collect

func listProcPIDs(dst []int32, _ *[]byte) ([]int32, bool) { return dst[:0], false }

func readProcSample(int32, bool, *[]byte) procCounters { return procCounters{} }

func readProcCmdline(int32) string { return "" }

func readProcOwners(int32) (uint32, uint32, bool, bool) { return 0, 0, false, false }
