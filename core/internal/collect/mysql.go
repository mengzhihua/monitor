package collect

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// mysqlConfig is collectors.modules.mysql (Netdata go.d mysql).
type mysqlConfig struct {
	Address  string        `yaml:"address"` // host:port or unix:///path
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type mysqlCollector struct {
	cfg mysqlConfig
}

func init() {
	Register("mysql", func() Collector { return &mysqlCollector{} })
}

func (m *mysqlCollector) Name() string { return "mysql" }

func (m *mysqlCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Address == "" {
		m.cfg.Address = "127.0.0.1:3306"
	}
	if m.cfg.User == "" {
		m.cfg.User = "root"
	}
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (m *mysqlCollector) Init(reg *registry.Registry) error {
	if m.cfg.Address == "" {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := m.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "mysql.queries", Title: "MySQL queries", Units: "queries/s", Priority: 45000,
			Dimensions: []*registry.Dimension{{ID: "queries", Algorithm: inc}, {ID: "questions", Algorithm: inc}, {ID: "slow_queries", Algorithm: inc}}},
		{ID: "mysql.handlers", Title: "MySQL handlers", Units: "handlers/s", Priority: 45010,
			Dimensions: []*registry.Dimension{
				{ID: "read_first", Algorithm: inc}, {ID: "read_key", Algorithm: inc}, {ID: "read_next", Algorithm: inc},
				{ID: "read_prev", Algorithm: inc}, {ID: "read_rnd", Algorithm: inc}, {ID: "read_rnd_next", Algorithm: inc},
				{ID: "write", Algorithm: inc}, {ID: "update", Algorithm: inc}, {ID: "delete", Algorithm: inc}}},
		{ID: "mysql.threads", Title: "MySQL threads", Units: "threads", Priority: 45020,
			Dimensions: []*registry.Dimension{{ID: "connected"}, {ID: "running"}, {ID: "cached"}, {ID: "created", Algorithm: inc}}},
		{ID: "mysql.connections", Title: "MySQL connections", Units: "connections/s", Priority: 45030,
			Dimensions: []*registry.Dimension{{ID: "accepted", Algorithm: inc}, {ID: "aborted", Algorithm: inc}}},
		{ID: "mysql.net", Title: "MySQL bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 45040,
			Dimensions: []*registry.Dimension{
				{ID: "in", Algorithm: inc, Multiplier: 8, Divisor: 1000},
				{ID: "out", Algorithm: inc, Multiplier: -8, Divisor: 1000}}},
		{ID: "mysql.innodb_io", Title: "MySQL InnoDB I/O", Units: "KiB/s", Type: registry.Area, Priority: 45050,
			Dimensions: []*registry.Dimension{
				{ID: "read", Algorithm: inc, Divisor: 1024}, {ID: "write", Algorithm: inc, Divisor: 1024}}},
		{ID: "mysql.innodb_rows", Title: "MySQL InnoDB row operations", Units: "operations/s", Priority: 45060,
			Dimensions: []*registry.Dimension{
				{ID: "read", Algorithm: inc}, {ID: "inserted", Algorithm: inc},
				{ID: "updated", Algorithm: inc}, {ID: "deleted", Algorithm: inc}}},
		{ID: "mysql.innodb_buffer_pool_pages", Title: "MySQL InnoDB buffer pool pages", Units: "pages", Type: registry.Stacked, Priority: 45070,
			Dimensions: []*registry.Dimension{{ID: "data"}, {ID: "dirty"}, {ID: "free"}, {ID: "misc"}}},
		{ID: "mysql.table_locks", Title: "MySQL table locks", Units: "locks/s", Priority: 45080,
			Dimensions: []*registry.Dimension{{ID: "immediate", Algorithm: inc}, {ID: "waited", Algorithm: inc}}},
		{ID: "mysql.tmp", Title: "MySQL temporary objects", Units: "objects/s", Priority: 45090,
			Dimensions: []*registry.Dimension{{ID: "disk_tables", Algorithm: inc}, {ID: "files", Algorithm: inc}, {ID: "tables", Algorithm: inc}}},
		{ID: "mysql.opened_tables", Title: "MySQL opened tables", Units: "tables/s", Priority: 45100,
			Dimensions: []*registry.Dimension{{ID: "opened", Algorithm: inc}}},
		{ID: "mysql.sorts", Title: "MySQL sorts", Units: "operations/s", Priority: 45110,
			Dimensions: []*registry.Dimension{{ID: "rows", Algorithm: inc}, {ID: "range", Algorithm: inc}, {ID: "merge_passes", Algorithm: inc}, {ID: "scan", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "mysql", "mysql", "mysql"
		reg.AddChart(c)
	}
	return nil
}

func (m *mysqlCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := m.status(ctx)
	if err != nil {
		return err
	}
	n := func(k string) float64 { return mysqlNum(s, k) }
	_ = reg.Collect("mysql.queries", now, map[string]float64{"queries": n("Queries"), "questions": n("Questions"), "slow_queries": n("Slow_queries")})
	_ = reg.Collect("mysql.handlers", now, map[string]float64{
		"read_first": n("Handler_read_first"), "read_key": n("Handler_read_key"), "read_next": n("Handler_read_next"),
		"read_prev": n("Handler_read_prev"), "read_rnd": n("Handler_read_rnd"), "read_rnd_next": n("Handler_read_rnd_next"),
		"write": n("Handler_write"), "update": n("Handler_update"), "delete": n("Handler_delete"),
	})
	_ = reg.Collect("mysql.threads", now, map[string]float64{
		"connected": n("Threads_connected"), "running": n("Threads_running"),
		"cached": n("Threads_cached"), "created": n("Threads_created"),
	})
	_ = reg.Collect("mysql.connections", now, map[string]float64{"accepted": n("Connections"), "aborted": n("Aborted_connects")})
	_ = reg.Collect("mysql.net", now, map[string]float64{"in": n("Bytes_received"), "out": n("Bytes_sent")})
	_ = reg.Collect("mysql.innodb_io", now, map[string]float64{"read": n("Innodb_data_read"), "write": n("Innodb_data_written")})
	_ = reg.Collect("mysql.innodb_rows", now, map[string]float64{
		"read": n("Innodb_rows_read"), "inserted": n("Innodb_rows_inserted"),
		"updated": n("Innodb_rows_updated"), "deleted": n("Innodb_rows_deleted"),
	})
	_ = reg.Collect("mysql.innodb_buffer_pool_pages", now, map[string]float64{
		"data": n("Innodb_buffer_pool_pages_data"), "dirty": n("Innodb_buffer_pool_pages_dirty"),
		"free": n("Innodb_buffer_pool_pages_free"), "misc": n("Innodb_buffer_pool_pages_misc"),
	})
	_ = reg.Collect("mysql.table_locks", now, map[string]float64{"immediate": n("Table_locks_immediate"), "waited": n("Table_locks_waited")})
	_ = reg.Collect("mysql.tmp", now, map[string]float64{"disk_tables": n("Created_tmp_disk_tables"), "files": n("Created_tmp_files"), "tables": n("Created_tmp_tables")})
	_ = reg.Collect("mysql.opened_tables", now, map[string]float64{"opened": n("Opened_tables")})
	_ = reg.Collect("mysql.sorts", now, map[string]float64{
		"rows": n("Sort_rows"), "range": n("Sort_range"), "merge_passes": n("Sort_merge_passes"), "scan": n("Sort_scan"),
	})
	return nil
}

func mysqlNum(m map[string]string, k string) float64 {
	v, _ := strconv.ParseFloat(m[k], 64)
	return v
}

func (m *mysqlCollector) status(ctx context.Context) (map[string]string, error) {
	addrs := []string{m.cfg.Address}
	if !strings.HasPrefix(m.cfg.Address, "unix://") {
		addrs = append(addrs, "unix:///var/run/mysqld/mysqld.sock", "unix:///tmp/mysql.sock")
	}
	var last error
	for _, a := range addrs {
		rows, err := mysqlQuery(ctx, a, m.cfg.User, m.cfg.Password, m.cfg.Timeout, "SHOW GLOBAL STATUS")
		if err == nil {
			out := map[string]string{}
			for _, r := range rows {
				if len(r) >= 2 {
					out[r[0]] = r[1]
				}
			}
			return out, nil
		}
		last = err
		if !isConnRefused(err) {
			return nil, err
		}
	}
	return nil, last
}

func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "no such file") ||
		strings.Contains(s, "connect: ") || os.IsNotExist(err)
}

func mysqlQuery(ctx context.Context, address, user, password string, timeout time.Duration, sql string) ([][]string, error) {
	d := net.Dialer{Timeout: timeout}
	network, addr := "tcp", address
	if strings.HasPrefix(address, "unix://") {
		network, addr = "unix", strings.TrimPrefix(address, "unix://")
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	hs, err := mysqlReadPacket(conn)
	if err != nil {
		return nil, err
	}
	plugin, scramble, err := mysqlParseHandshake(hs)
	if err != nil {
		return nil, err
	}
	auth, plugin := mysqlAuth(plugin, scramble, password)
	if err := mysqlWritePacket(conn, 1, mysqlHandshakeResp(user, auth, plugin)); err != nil {
		return nil, err
	}
	resp, err := mysqlReadPacket(conn)
	if err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("mysql: empty auth reply")
	}
	if resp[0] == 0xfe { // auth switch
		plugin, scramble = mysqlParseAuthSwitch(resp)
		auth, plugin = mysqlAuth(plugin, scramble, password)
		if err := mysqlWritePacket(conn, 3, auth); err != nil {
			return nil, err
		}
		resp, err = mysqlReadPacket(conn)
		if err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			return nil, fmt.Errorf("mysql: empty auth-switch reply")
		}
	}
	if resp[0] == 0xff {
		return nil, mysqlError(resp)
	}
	if resp[0] == 0x01 && len(resp) > 1 && resp[1] == 0x04 {
		// caching_sha2 full auth requested; not supported without RSA/TLS
		return nil, fmt.Errorf("mysql: server requested caching_sha2 full auth (set mysql_native_password or use TLS)")
	}
	if resp[0] != 0x00 && resp[0] != 0xfe {
		return nil, fmt.Errorf("mysql: unexpected auth reply 0x%02x", resp[0])
	}
	payload := append([]byte{0x03}, []byte(sql)...)
	if err := mysqlWritePacket(conn, 0, payload); err != nil {
		return nil, err
	}
	return mysqlReadResult(conn)
}

func mysqlReadPacket(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func mysqlWritePacket(w io.Writer, seq byte, payload []byte) error {
	var hdr [4]byte
	n := len(payload)
	hdr[0], hdr[1], hdr[2], hdr[3] = byte(n), byte(n>>8), byte(n>>16), seq
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func mysqlParseHandshake(b []byte) (plugin string, scramble []byte, err error) {
	if len(b) < 20 || b[0] != 10 {
		return "", nil, fmt.Errorf("mysql: not a handshake")
	}
	i := 1
	for i < len(b) && b[i] != 0 {
		i++
	}
	i++ // skip version
	if i+13 > len(b) {
		return "", nil, fmt.Errorf("mysql: truncated handshake")
	}
	i += 4 // thread id
	scramble = append([]byte{}, b[i:i+8]...)
	i += 8 + 1 // scramble-1 + filler
	if i+5 > len(b) {
		return "mysql_native_password", scramble, nil
	}
	caps := uint32(b[i]) | uint32(b[i+1])<<8
	i += 2 + 1 + 2 // charset + status
	if i+2 > len(b) {
		return "mysql_native_password", scramble, nil
	}
	caps |= uint32(b[i])<<16 | uint32(b[i+1])<<24
	i += 2
	authLen := 21
	if i < len(b) {
		if b[i] > 0 {
			authLen = int(b[i])
		}
		i++
	}
	i += 10 // reserved
	rest := authLen - 8
	if rest < 13 {
		rest = 13
	}
	if i+rest <= len(b) {
		part2 := b[i : i+rest]
		if n := len(part2); n > 0 && part2[n-1] == 0 {
			part2 = part2[:n-1]
		}
		scramble = append(scramble, part2...)
		i += rest
	}
	if i < len(b) {
		plugin = strings.TrimRight(string(b[i:]), "\x00")
	}
	if plugin == "" {
		plugin = "mysql_native_password"
	}
	return plugin, scramble, nil
}

func mysqlParseAuthSwitch(b []byte) (plugin string, scramble []byte) {
	// 0xfe plugin\0 scramble\0
	rest := b[1:]
	plugin, restb, _ := strings.Cut(string(rest), "\x00")
	scramble = []byte(strings.TrimRight(restb, "\x00"))
	return plugin, scramble
}

func mysqlAuth(plugin string, scramble []byte, password string) ([]byte, string) {
	if password == "" {
		return []byte{}, plugin
	}
	switch plugin {
	case "caching_sha2_password":
		return mysqlSHA2(scramble, password), plugin
	default:
		return mysqlNative(scramble, password), "mysql_native_password"
	}
}

func mysqlNative(scramble []byte, password string) []byte {
	if len(scramble) > 20 {
		scramble = scramble[:20]
	}
	h1 := sha1.Sum([]byte(password))
	h2 := sha1.Sum(h1[:])
	h3 := sha1.New()
	h3.Write(scramble)
	h3.Write(h2[:])
	h := h3.Sum(nil)
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		out[i] = h1[i] ^ h[i]
	}
	return out
}

func mysqlSHA2(scramble []byte, password string) []byte {
	if len(scramble) > 20 {
		scramble = scramble[:20]
	}
	h1 := sha256.Sum256([]byte(password))
	h2 := sha256.Sum256(h1[:])
	h3 := sha256.New()
	h3.Write(h2[:])
	h3.Write(scramble)
	h := h3.Sum(nil)
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		out[i] = h1[i] ^ h[i]
	}
	return out
}

func mysqlHandshakeResp(user string, auth []byte, plugin string) []byte {
	const caps = 0x000fa28d // PROTOCOL41 | SECURE_CONNECTION | PLUGIN_AUTH | TRANSACTIONS | LONG_PASSWORD | ...
	buf := make([]byte, 4+4+1+23)
	binary.LittleEndian.PutUint32(buf[0:], caps)
	binary.LittleEndian.PutUint32(buf[4:], 1<<24)
	buf[8] = 33 // utf8
	buf = append(buf, append([]byte(user), 0)...)
	if len(auth) < 256 {
		buf = append(buf, byte(len(auth)))
		buf = append(buf, auth...)
	} else {
		buf = append(buf, 0)
	}
	buf = append(buf, append([]byte(plugin), 0)...)
	return buf
}

func mysqlError(b []byte) error {
	if len(b) < 3 {
		return fmt.Errorf("mysql: error")
	}
	code := int(b[1]) | int(b[2])<<8
	msg := ""
	if len(b) > 9 {
		msg = string(b[9:])
	} else if len(b) > 3 {
		msg = string(b[3:])
	}
	return fmt.Errorf("mysql: %d %s", code, msg)
}

func mysqlReadResult(r io.Reader) ([][]string, error) {
	first, err := mysqlReadPacket(r)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 {
		return nil, fmt.Errorf("mysql: empty result")
	}
	if first[0] == 0xff {
		return nil, mysqlError(first)
	}
	if first[0] == 0x00 { // OK, no result set
		return nil, nil
	}
	cols, _ := mysqlLenenc(first)
	for i := 0; i < int(cols); i++ { // column defs
		if _, err := mysqlReadPacket(r); err != nil {
			return nil, err
		}
	}
	// optional EOF after columns (not sent when CLIENT_DEPRECATE_EOF)
	pkt, err := mysqlReadPacket(r)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for {
		if len(pkt) == 0 {
			return rows, nil
		}
		if pkt[0] == 0xfe && len(pkt) < 9 { // EOF
			return rows, nil
		}
		if pkt[0] == 0xff {
			return nil, mysqlError(pkt)
		}
		row, err := mysqlParseRow(pkt, int(cols))
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
		pkt, err = mysqlReadPacket(r)
		if err != nil {
			return nil, err
		}
	}
}

func mysqlParseRow(b []byte, cols int) ([]string, error) {
	out := make([]string, 0, cols)
	i := 0
	for len(out) < cols && i < len(b) {
		if b[i] == 0xfb { // NULL
			out = append(out, "")
			i++
			continue
		}
		n, sz := mysqlLenenc(b[i:])
		if sz == 0 {
			break
		}
		i += sz
		if i+int(n) > len(b) {
			return nil, fmt.Errorf("mysql: truncated row")
		}
		out = append(out, string(b[i:i+int(n)]))
		i += int(n)
	}
	return out, nil
}

func mysqlLenenc(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	switch b[0] {
	case 0xfc:
		if len(b) < 3 {
			return 0, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8, 3
	case 0xfd:
		if len(b) < 4 {
			return 0, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16, 4
	case 0xfe:
		if len(b) < 9 {
			return 0, 0
		}
		return binary.LittleEndian.Uint64(b[1:9]), 9
	case 0xff, 0xfb:
		return 0, 0
	default:
		return uint64(b[0]), 1
	}
}
