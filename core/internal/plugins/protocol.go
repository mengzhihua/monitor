// Package plugins runs external collectors that speak the plugins.d text
// protocol over stdout (the same line protocol Netdata external plugins use),
// so collectors can be written in any language.
//
//	CHART type.id name title units [family [context [charttype [priority [update_every [options [plugin [module]]]]]]]]
//	DIMENSION id [name [algorithm [multiplier [divisor [options]]]]]
//	CLABEL name value [source]        (before CLABEL_COMMIT, applies to the last CHART)
//	CLABEL_COMMIT
//	BEGIN type.id [microseconds]
//	SET id = value
//	END
//	FLUSH                              (drop the current BEGIN block)
//	VARIABLE [HOST|CHART] name = value
//	DISABLE                            (plugin asks not to be restarted)
//	EXIT
package plugins

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ErrDisabled is returned by Parser.Run when the plugin sent DISABLE.
var ErrDisabled = errors.New("plugin disabled itself")

// ErrExit is returned when the plugin sent EXIT (clean stop, may restart).
var ErrExit = errors.New("plugin requested exit")

// Stats counts what a parser has seen; read through Manager status.
type Stats struct {
	Lines     uint64 `json:"lines"`
	Samples   uint64 `json:"samples"`
	Charts    int    `json:"charts"`
	Errors    uint64 `json:"errors"`
	LastError string `json:"last_error,omitempty"`
	LastData  int64  `json:"last_data,omitempty"`
}

// Parser turns one plugin's output stream into registry charts and samples.
type Parser struct {
	Plugin string
	Reg    *registry.Registry
	// Now supplies collection timestamps (defaults to time.Now).
	Now func() time.Time
	// MaxLine caps a single protocol line (default 64 KiB).
	MaxLine int

	mu        sync.Mutex
	stats     Stats
	charts    map[string]*registry.Chart
	draft     *registry.Chart   // last CHART, registered once its DIMENSION/CLABEL lines are done
	pending   map[string]string // CLABELs awaiting CLABEL_COMMIT
	variables map[string]float64
	functions []string
	replace   bool

	cur     *registry.Chart // inside BEGIN..END
	curVals map[string]float64
	curTS   time.Time
	last    time.Time // last line seen (watchdog)
}

func (p *Parser) touch() {
	p.mu.Lock()
	p.last = time.Now()
	p.mu.Unlock()
}

func (p *Parser) lastActivity() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// Stats returns a snapshot of the parser counters.
func (p *Parser) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.Charts = len(p.charts)
	return s
}

// Variables returns the values published with VARIABLE.
func (p *Parser) Variables() map[string]float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]float64, len(p.variables))
	for k, v := range p.variables {
		out[k] = v
	}
	return out
}

// Functions returns names declared with FUNCTION. The agent does not attach
// the plugin stdin, so these names are reported but not invoked.
func (p *Parser) Functions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.functions...)
}

// Run consumes r until EOF, DISABLE or EXIT. Protocol errors are counted and
// skipped; only I/O errors and the two control commands end the run.
func (p *Parser) Run(r io.Reader) error {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.MaxLine <= 0 {
		p.MaxLine = 64 << 10
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), p.MaxLine)
	for sc.Scan() {
		if err := p.Line(sc.Text()); err != nil {
			if errors.Is(err, ErrDisabled) || errors.Is(err, ErrExit) {
				return err
			}
			p.mu.Lock()
			p.stats.Errors++
			p.stats.LastError = err.Error()
			p.mu.Unlock()
		}
	}
	p.mu.Lock()
	p.commitDraft()
	p.mu.Unlock()
	return sc.Err()
}

// Line handles one protocol line.
func (p *Parser) Line(line string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.charts == nil {
		p.charts = map[string]*registry.Chart{}
		p.variables = map[string]float64{}
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	p.stats.Lines++
	p.last = time.Now()
	f := fields(line)
	if len(f) == 0 || strings.HasPrefix(f[0], "#") {
		return nil
	}
	cmd := strings.ToUpper(f[0])
	if cmd != "DIMENSION" && cmd != "CLABEL" && cmd != "CLABEL_COMMIT" && cmd != "LABEL" {
		p.commitDraft()
	}
	switch cmd {
	case "CHART":
		p.replace = false
		return p.chart(f[1:])
	case "OVERWRITE":
		p.replace = true
		return p.chart(f[1:])
	case "DIMENSION":
		return p.dimension(f[1:])
	case "CLABEL":
		if len(f) < 3 {
			return fmt.Errorf("CLABEL: need name and value")
		}
		if p.draft == nil {
			return fmt.Errorf("CLABEL before CHART")
		}
		if p.pending == nil {
			p.pending = map[string]string{}
		}
		p.pending[f[1]] = f[2]
	case "CLABEL_COMMIT":
		if p.draft == nil {
			return fmt.Errorf("CLABEL_COMMIT: no chart")
		}
		if len(p.pending) > 0 {
			if p.draft.Labels == nil {
				p.draft.Labels = map[string]string{}
			}
			for k, v := range p.pending {
				p.draft.Labels[k] = v
			}
		}
		p.pending = nil
	case "LABEL":
		if len(f) < 3 {
			return fmt.Errorf("LABEL: need name and value")
		}
		if p.draft == nil {
			return fmt.Errorf("LABEL before CHART")
		}
		if p.draft.Labels == nil {
			p.draft.Labels = map[string]string{}
		}
		p.draft.Labels[f[1]] = f[2]
	case "HOST_LABEL":
		if len(f) < 3 {
			return fmt.Errorf("HOST_LABEL: need name and value")
		}
		p.Reg.SetHostLabel(f[1], f[2])
	case "FUNCTION":
		if len(f) < 2 || f[1] == "" {
			return fmt.Errorf("FUNCTION: missing name")
		}
		for _, name := range p.functions {
			if name == f[1] {
				return nil
			}
		}
		p.functions = append(p.functions, f[1])
	case "BEGIN":
		if len(f) < 2 {
			return fmt.Errorf("BEGIN: missing chart id")
		}
		c, ok := p.charts[f[1]]
		if !ok {
			p.cur = nil
			return fmt.Errorf("BEGIN: unknown chart %q", f[1])
		}
		p.cur, p.curVals, p.curTS = c, map[string]float64{}, p.Now()
	case "SET":
		if p.cur == nil {
			return fmt.Errorf("SET outside BEGIN/END")
		}
		id, val, err := assignment(f[1:])
		if err != nil {
			return fmt.Errorf("SET: %w", err)
		}
		if val == "" {
			return nil // explicit gap
		}
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("SET %s: bad value %q", id, val)
		}
		if p.cur.Dimension(id) == nil {
			return fmt.Errorf("SET: unknown dimension %q on %s", id, p.cur.ID)
		}
		p.curVals[id] = v
	case "END":
		if p.cur == nil {
			return fmt.Errorf("END without BEGIN")
		}
		c, vals, ts := p.cur, p.curVals, p.curTS
		p.cur, p.curVals = nil, nil
		if len(vals) == 0 {
			return nil
		}
		p.stats.Samples += uint64(len(vals))
		p.stats.LastData = ts.Unix()
		p.mu.Unlock()
		err := p.Reg.Collect(c.ID, ts, vals)
		p.mu.Lock()
		return err
	case "FLUSH":
		p.cur, p.curVals = nil, nil
	case "VARIABLE":
		args := f[1:]
		if len(args) > 0 && (strings.EqualFold(args[0], "HOST") || strings.EqualFold(args[0], "CHART") || strings.EqualFold(args[0], "GLOBAL")) {
			args = args[1:]
		}
		name, val, err := assignment(args)
		if err != nil {
			return fmt.Errorf("VARIABLE: %w", err)
		}
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("VARIABLE %s: bad value %q", name, val)
		}
		p.variables[name] = v
	case "DISABLE":
		return ErrDisabled
	case "EXIT":
		return ErrExit
	case "HOST_DEFINE", "HOST_DEFINE_END", "HOST", "REPORT_JOB_STATUS":
		// accepted for Netdata compatibility; these do not create charts
	default:
		return fmt.Errorf("unknown command %q", f[0])
	}
	return nil
}

// assignment parses "name = value", "name=value" or "name = " (empty value).
func assignment(f []string) (name, value string, err error) {
	joined := strings.Join(f, " ")
	i := strings.IndexByte(joined, '=')
	if i < 0 {
		return "", "", fmt.Errorf("expected name = value, got %q", joined)
	}
	name = strings.TrimSpace(joined[:i])
	value = strings.TrimSpace(joined[i+1:])
	if name == "" {
		return "", "", fmt.Errorf("empty name in %q", joined)
	}
	return name, value, nil
}

func (p *Parser) chart(f []string) error {
	if len(f) < 1 || f[0] == "" {
		return fmt.Errorf("CHART: missing id")
	}
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	c := &registry.Chart{
		ID:      f[0],
		Title:   get(2),
		Units:   get(3),
		Family:  get(4),
		Context: get(5),
		Plugin:  p.Plugin,
		Module:  get(11),
	}
	if c.Units == "" {
		c.Units = "value"
	}
	if c.Family == "" {
		if i := strings.IndexByte(c.ID, '.'); i > 0 {
			c.Family = c.ID[:i]
		} else {
			c.Family = c.ID
		}
	}
	switch strings.ToLower(get(6)) {
	case "area":
		c.Type = registry.Area
	case "stacked":
		c.Type = registry.Stacked
	default:
		c.Type = registry.Line
	}
	if n, err := strconv.Atoi(get(7)); err == nil && n > 0 {
		c.Priority = n
	}
	if n, err := strconv.Atoi(get(8)); err == nil && n > 0 {
		c.UpdateEvery = n
	}
	if get(10) != "" {
		c.Plugin = p.Plugin + "/" + get(10)
	}
	p.draft = c
	p.pending = nil
	return nil
}

// commitDraft registers the pending CHART. A chart re-declared after a
// plugin restart keeps its registry instance; only new dimensions are added.
func (p *Parser) commitDraft() {
	c := p.draft
	if c == nil {
		return
	}
	replace := p.replace
	p.draft, p.pending, p.replace = nil, nil, false
	dims := c.Dimensions
	var stored *registry.Chart
	if replace {
		stored = p.Reg.ReplaceChart(c)
	} else {
		stored = p.Reg.AddChart(c)
		if stored != c {
			for _, d := range dims {
				stored.AddDimension(d)
			}
			stored.MergeLabels(c.Labels)
		}
	}
	p.charts[c.ID] = stored
}

func (p *Parser) dimension(f []string) error {
	if p.draft == nil {
		return fmt.Errorf("DIMENSION before CHART")
	}
	if len(f) < 1 || f[0] == "" {
		return fmt.Errorf("DIMENSION: missing id")
	}
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	d := &registry.Dimension{ID: f[0], Name: get(1)}
	switch strings.ToLower(get(2)) {
	case "incremental":
		d.Algorithm = registry.Incremental
	case "percentage-of-absolute-row":
		d.Algorithm = registry.PercentageOfAbsoluteRow
	case "percentage-of-incremental-row":
		d.Algorithm = registry.PercentageOfIncrementalRow
	default:
		d.Algorithm = registry.Absolute
	}
	if n, err := strconv.ParseInt(get(3), 10, 64); err == nil && n != 0 {
		d.Multiplier = n
	}
	if n, err := strconv.ParseInt(get(4), 10, 64); err == nil && n != 0 {
		d.Divisor = n
	}
	if strings.Contains(get(5), "hidden") {
		d.Hidden = true
	}
	p.draft.Dimensions = append(p.draft.Dimensions, d)
	return nil
}

// fields splits on whitespace honouring single and double quotes.
func fields(s string) []string {
	var out []string
	var cur strings.Builder
	inTok := false
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote, inTok = ch, true
		case ch == ' ' || ch == '\t' || ch == '\r':
			if inTok {
				out = append(out, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			cur.WriteByte(ch)
			inTok = true
		}
	}
	if inTok {
		out = append(out, cur.String())
	}
	return out
}
