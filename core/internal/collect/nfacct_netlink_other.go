//go:build !linux

package collect

func readNfacctNetlink() ([]nfacctObj, bool) { return nil, false }
