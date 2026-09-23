//go:build !linux

package collect

func (p *profileCollector) agentCPU() (user, sys float64, ok bool) {
	return 0, 0, false
}
