package collect

import (
	"errors"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// noteUnsupported records a not_supported preprocessing result on the chart.
// It returns true when err is non-nil and the sample should be skipped.
func noteUnsupported(reg *registry.Registry, chartID string, err error) bool {
	ch, ok := reg.Chart(chartID)
	if err == nil {
		if ok {
			ch.ClearUnsupported()
		}
		return false
	}
	if ok && errors.Is(err, preprocess.ErrNotSupported) {
		ch.SetUnsupported(err.Error())
	}
	return true
}
