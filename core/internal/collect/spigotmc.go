package collect

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// spigotmcConfig is collectors.modules.spigotmc (Minecraft RCON :25575).
type spigotmcConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type spigotmcCollector struct {
	cfg  spigotmcConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	cmd  func(ctx context.Context, command string) (string, error)
}

func init() {
	Register("spigotmc", func() Collector { return &spigotmcCollector{} })
}

var (
	reSpigotTPS  = regexp.MustCompile(`(?ms)(?P<tps_1min>\d+\.\d+),.*?(?P<tps_5min>\d+\.\d+),.*?(?P<tps_15min>\d+\.\d+).*?$.*?(?P<mem_used>\d+)/(?P<mem_alloc>\d+)[^:]+:\s*(?P<mem_max>\d+)`)
	reSpigotList = regexp.MustCompile(`(?P<players>\d+)/?(?P<hidden_players>\d+)?.*?(?P<total_players>\d+)`)
	reSpigotCode = regexp.MustCompile(`§.`)
)

func (s *spigotmcCollector) Name() string { return "spigotmc" }

func (s *spigotmcCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Address == "" {
		s.cfg.Address = "127.0.0.1:25575"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (s *spigotmcCollector) Init(reg *registry.Registry) error {
	if s.cfg.Address == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := s.stats(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "spigotmc.players", Title: "Active Players", Units: "players", Priority: 60600,
			Dimensions: []*registry.Dimension{{ID: "players"}}},
		{ID: "spigotmc.avg_tps", Title: "Average Ticks Per Second", Units: "ticks", Priority: 60610,
			Dimensions: []*registry.Dimension{{ID: "1min"}, {ID: "5min"}, {ID: "15min"}}},
		{ID: "spigotmc.memory", Title: "Memory Usage", Units: "bytes", Type: registry.Area, Priority: 60620,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "alloc"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "spigotmc", "spigotmc", "spigotmc"
		reg.AddChart(ch)
	}
	return nil
}

func (s *spigotmcCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := s.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("spigotmc.players", now, map[string]float64{"players": st["players"]})
	_ = reg.Collect("spigotmc.avg_tps", now, map[string]float64{"1min": st["tps_1min"], "5min": st["tps_5min"], "15min": st["tps_15min"]})
	_ = reg.Collect("spigotmc.memory", now, map[string]float64{"used": st["mem_used"], "alloc": st["mem_alloc"]})
	return nil
}

func (s *spigotmcCollector) stats(ctx context.Context) (map[string]float64, error) {
	tps, err := s.query(ctx, "tps")
	if err != nil {
		return nil, fmt.Errorf("spigotmc: %w", err)
	}
	list, err := s.query(ctx, "list")
	if err != nil {
		return nil, fmt.Errorf("spigotmc: %w", err)
	}
	tps = reSpigotCode.ReplaceAllString(tps, "")
	list = reSpigotCode.ReplaceAllString(list, "")
	mx := map[string]float64{}
	m := reSpigotTPS.FindStringSubmatch(tps)
	if m == nil {
		return nil, fmt.Errorf("spigotmc: tps regexp does not match")
	}
	for i, name := range reSpigotTPS.SubexpNames() {
		if name == "" || i >= len(m) || m[i] == "" {
			continue
		}
		v, err := strconv.ParseFloat(m[i], 64)
		if err != nil {
			return nil, fmt.Errorf("spigotmc: %w", err)
		}
		if strings.HasPrefix(name, "mem") {
			v *= 1024 * 1024
		}
		mx[name] = v
	}
	lm := reSpigotList.FindStringSubmatch(list)
	if lm == nil {
		return nil, fmt.Errorf("spigotmc: list regexp does not match")
	}
	var players float64
	for i, name := range reSpigotList.SubexpNames() {
		if (name == "players" || name == "hidden_players") && i < len(lm) && lm[i] != "" {
			v, _ := strconv.ParseFloat(lm[i], 64)
			players += v
		}
	}
	mx["players"] = players
	return mx, nil
}

func (s *spigotmcCollector) query(ctx context.Context, command string) (string, error) {
	if s.cmd != nil {
		return s.cmd(ctx, command)
	}
	return s.rcon(ctx, command)
}

func (s *spigotmcCollector) rcon(ctx context.Context, command string) (string, error) {
	dial := s.dial
	if dial == nil {
		d := net.Dialer{Timeout: s.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", s.cfg.Address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.cfg.Timeout))
	if err := rconWrite(conn, 1, 3, s.cfg.Password); err != nil {
		return "", err
	}
	id, typ, _, err := rconRead(conn)
	if err != nil {
		return "", err
	}
	if typ != 2 || id == -1 {
		return "", fmt.Errorf("rcon auth failed")
	}
	if err := rconWrite(conn, 2, 2, command); err != nil {
		return "", err
	}
	_, _, body, err := rconRead(conn)
	if err != nil {
		return "", err
	}
	return body, nil
}

func rconWrite(w io.Writer, id, typ int32, body string) error {
	payload := append([]byte(body), 0, 0)
	length := int32(8 + len(payload))
	buf := make([]byte, 4+int(length))
	binary.LittleEndian.PutUint32(buf[0:], uint32(length))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], payload)
	_, err := w.Write(buf)
	return err
}

func rconRead(r io.Reader) (id, typ int32, body string, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	n := int(binary.LittleEndian.Uint32(hdr[:]))
	if n < 8 || n > 1<<20 {
		err = fmt.Errorf("rcon: bad length %d", n)
		return
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(r, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:8]))
	payload := buf[8:]
	if i := bytesIndexByte(payload, 0); i >= 0 {
		body = string(payload[:i])
	} else {
		body = string(payload)
	}
	return
}

func bytesIndexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
