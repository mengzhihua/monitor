package api

import (
	"fmt"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	label := q.Get("label")
	units := q.Get("units")
	color := "#64748b"
	var value float64
	have := false

	if alarm := q.Get("alarm"); alarm != "" {
		var alarms []health.Alarm
		if v.node != nil {
			for _, e := range v.node.Alarms() {
				alarms = append(alarms, alarmFromEntry(e))
			}
		} else if s.opt.Health != nil {
			alarms = s.opt.Health.Alarms()
		}
		for _, a := range alarms {
			if a.Name != alarm && a.Chart+"."+a.Name != alarm {
				continue
			}
			if !math.IsNaN(a.Value) {
				value, have = a.Value, true
			}
			if units == "" {
				units = a.Units
			}
			if label == "" {
				label = a.Name
			}
			switch a.Status {
			case health.StatusCritical:
				color = "#b91c1c"
			case health.StatusWarning:
				color = "#ca8a04"
			case health.StatusClear:
				color = "#15803d"
			}
			break
		}
	}
	if !have {
		charts, errMsg := chartsForData(v.reg, q.Get("chart"), q.Get("context"))
		if errMsg != "" {
			http.Error(w, errMsg, http.StatusNotFound)
			return
		}
		want := q.Get("dimension")
		if want == "" {
			want = q.Get("dimensions")
			if i := strings.Index(want, ","); i >= 0 {
				want = want[:i]
			}
		}
		sum, n := 0.0, 0
		for _, c := range charts {
			if label == "" {
				label = c.ID
			}
			if units == "" {
				units = c.Units
			}
			_, last := c.LastValues()
			if want != "" {
				if val, ok := last[want]; ok {
					sum += val
					n++
				}
				continue
			}
			for _, d := range c.Dims() {
				if d.Hidden {
					continue
				}
				if val, ok := last[d.ID]; ok {
					sum += val
					n++
					break
				}
			}
		}
		if n > 0 {
			value, have = sum, true
			if n > 1 {
				color = "#2563eb"
			} else {
				color = "#15803d"
			}
		}
	}
	if vc := q.Get("value_color"); vc != "" {
		if c := sanitizeColor(vc); c != "" {
			color = c
		}
	}
	prec := 2
	if p, err := strconv.Atoi(q.Get("precision")); err == nil && p >= 0 && p <= 6 {
		prec = p
	}
	valueText := "-"
	if have {
		valueText = strconv.FormatFloat(roundN(value, prec), 'f', prec, 64)
		if units != "" {
			valueText += " " + units
		}
	}
	if label == "" {
		label = "monitor"
	}
	writeBadgeSVG(w, label, valueText, color)
}

func roundN(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}

func sanitizeColor(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "red":
		return "#b91c1c"
	case "green":
		return "#15803d"
	case "orange", "yellow":
		return "#ca8a04"
	case "blue":
		return "#2563eb"
	case "gray", "grey":
		return "#64748b"
	}
	if strings.HasPrefix(s, "#") && (len(s) == 4 || len(s) == 7) {
		for _, r := range s[1:] {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return ""
			}
		}
		return s
	}
	return ""
}

func writeBadgeSVG(w http.ResponseWriter, label, value, color string) {
	lw := 7*len(label) + 10
	vw := 7*len(value) + 10
	if lw < 40 {
		lw = 40
	}
	if vw < 30 {
		vw = 30
	}
	width := lw + vw
	escL, escV := html.EscapeString(label), html.EscapeString(value)
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, width, escL, escV)
	fmt.Fprintf(w, `<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#fff" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`)
	fmt.Fprintf(w, `<rect rx="3" width="%d" height="20" fill="#555"/>`, width)
	fmt.Fprintf(w, `<rect rx="3" x="%d" width="%d" height="20" fill="%s"/>`, lw, vw, html.EscapeString(color))
	fmt.Fprintf(w, `<rect x="%d" width="4" height="20" fill="%s"/>`, lw, html.EscapeString(color))
	fmt.Fprintf(w, `<rect rx="3" width="%d" height="20" fill="url(#s)"/>`, width)
	fmt.Fprintf(w, `<g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">`)
	fmt.Fprintf(w, `<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text>`, lw/2, escL, lw/2, escL)
	fmt.Fprintf(w, `<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text>`, lw+vw/2, escV, lw+vw/2, escV)
	fmt.Fprintf(w, `</g></svg>`)
}
