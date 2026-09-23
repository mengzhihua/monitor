//go:build !linux

package collect

func readXenDomains() ([]xlDomain, bool) { return nil, false }
