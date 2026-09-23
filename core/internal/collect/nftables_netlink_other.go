//go:build !linux

package collect

func readNftCountersNetlink() ([]nftCounter, bool) { return nil, false }
