package plugins

import (
	"fmt"
	"strings"
)

func (p *Parser) checkLabel(key string) error {
	if p.Namespace == "" && p.MaxLabels <= 0 {
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
	budget := p.MaxLabels
	if budget <= 0 {
		budget = 128
	}
	if _, exists := labels[key]; !exists && len(labels) >= budget {
		return fmt.Errorf("LABEL: package label budget exceeded")
	}
	return nil
}

func (p *Parser) checkChart(id string) error {
	if p.Namespace != "" {
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
	} else if existing, ok := p.Reg.Chart(id); ok && (existing.Plugin == p.Plugin || strings.HasPrefix(existing.Plugin, p.Plugin+"/")) {
		return nil
	}
	if p.MaxCharts <= 0 || p.ownedCharts() < p.MaxCharts {
		return nil
	}
	return fmt.Errorf("CHART: package chart budget exceeded")
}

func (p *Parser) ownedCharts() int {
	count := 0
	for _, chart := range p.Reg.Charts() {
		if p.Namespace != "" {
			if strings.HasPrefix(chart.ID, p.Namespace) {
				count++
			}
			continue
		}
		if chart.Plugin == p.Plugin || strings.HasPrefix(chart.Plugin, p.Plugin+"/") {
			count++
		}
	}
	return count
}

func (p *Parser) checkDimension(id string) error {
	if p.Namespace == "" && p.MaxDimensions <= 0 {
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
