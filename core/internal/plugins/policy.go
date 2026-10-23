package plugins

import (
	"fmt"
	"strings"
)

func (p *Parser) checkLabel(key string) error {
	if p.Namespace == "" {
		return nil
	}
	labels := map[string]string{}
	if chart, ok := p.Reg.Chart(p.draft.ID); ok && !p.replace {
		labels = chart.LabelsSnapshot()
		if labels == nil {
			labels = map[string]string{}
		}
	}
	for k, v := range p.draft.Labels {
		labels[k] = v
	}
	for k, v := range p.pending {
		labels[k] = v
	}
	if _, exists := labels[key]; !exists && len(labels) >= 128 {
		return fmt.Errorf("LABEL: package label budget exceeded")
	}
	return nil
}

func (p *Parser) checkChart(id string) error {
	if p.Namespace == "" {
		return nil
	}
	if !strings.HasPrefix(id, p.Namespace) || len(id) <= len(p.Namespace) || len(id) > 200 {
		return fmt.Errorf("CHART: id must be inside %s with at most 200 bytes", p.Namespace)
	}
	if existing, ok := p.Reg.Chart(id); ok {
		// Namespace alone is insufficient if a legacy collector already owns
		// that name. Never attach dimensions to or overwrite another owner.
		if existing.Plugin != p.Plugin && !strings.HasPrefix(existing.Plugin, p.Plugin+"/") {
			return fmt.Errorf("CHART: id is owned by another collector")
		}
		return nil
	}
	count := 0
	for _, chart := range p.Reg.Charts() {
		if strings.HasPrefix(chart.ID, p.Namespace) {
			count++
		}
	}
	if p.MaxCharts > 0 && count >= p.MaxCharts {
		return fmt.Errorf("CHART: package chart budget exceeded")
	}
	return nil
}

func (p *Parser) checkDimension(id string) error {
	if p.Namespace == "" {
		return nil
	}
	if len(id) > 128 {
		return fmt.Errorf("DIMENSION: id exceeds 128 bytes")
	}
	ids := map[string]bool{}
	// Existing definitions are retained on redeclaration; account for their
	// dimensions too, so repeated process restarts cannot bypass the budget.
	if chart, ok := p.Reg.Chart(p.draft.ID); ok && !p.replace {
		for _, dimension := range chart.Dims() {
			ids[dimension.ID] = true
		}
	}
	for _, dimension := range p.draft.Dimensions {
		if dimension.ID == id {
			return fmt.Errorf("DIMENSION: duplicate id in chart declaration")
		}
		ids[dimension.ID] = true
	}
	if !ids[id] && p.MaxDimensions > 0 && len(ids) >= p.MaxDimensions {
		return fmt.Errorf("DIMENSION: package dimension budget exceeded")
	}
	return nil
}
