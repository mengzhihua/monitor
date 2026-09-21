package collect

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// postgresConfig is collectors.modules.postgres (Netdata go.d postgres).
type postgresConfig struct {
	Address  string        `yaml:"address"` // host:port or unix:///dir (dir contains .s.PGSQL.5432)
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Database string        `yaml:"database"`
	Timeout  time.Duration `yaml:"timeout"`
}

type postgresCollector struct {
	cfg postgresConfig
}

func init() {
	Register("postgres", func() Collector { return &postgresCollector{} })
}

func (p *postgresCollector) Name() string { return "postgres" }

func (p *postgresCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Address == "" {
		p.cfg.Address = "127.0.0.1:5432"
	}
	if p.cfg.User == "" {
		if u, err := user.Current(); err == nil {
			p.cfg.User = u.Username
		} else {
			p.cfg.User = "postgres"
		}
	}
	if p.cfg.Database == "" {
		p.cfg.Database = "postgres"
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (p *postgresCollector) Init(reg *registry.Registry) error {
	if p.cfg.Address == "" {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := p.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "postgres.connections", Title: "PostgreSQL connections", Units: "connections", Priority: 46000,
			Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "idle"}}},
		{ID: "postgres.db_transactions", Title: "PostgreSQL transactions", Units: "transactions/s", Priority: 46010,
			Dimensions: []*registry.Dimension{{ID: "committed", Algorithm: inc}, {ID: "rolled_back", Algorithm: inc}}},
		{ID: "postgres.db_tuples", Title: "PostgreSQL tuple operations", Units: "tuples/s", Priority: 46020,
			Dimensions: []*registry.Dimension{
				{ID: "fetched", Algorithm: inc}, {ID: "returned", Algorithm: inc},
				{ID: "inserted", Algorithm: inc}, {ID: "updated", Algorithm: inc}, {ID: "deleted", Algorithm: inc}}},
		{ID: "postgres.db_size", Title: "PostgreSQL database size", Units: "MiB", Priority: 46030, Type: registry.Area,
			Dimensions: []*registry.Dimension{{ID: "size", Divisor: 1 << 20}}},
		{ID: "postgres.bgwriter_buffers", Title: "PostgreSQL bgwriter buffers", Units: "buffers/s", Priority: 46040,
			Dimensions: []*registry.Dimension{
				{ID: "checkpoint", Algorithm: inc}, {ID: "backend", Algorithm: inc},
				{ID: "bgwriter", Algorithm: inc}, {ID: "allocated", Algorithm: inc}}},
		{ID: "postgres.bgwriter_checkpoint_time", Title: "PostgreSQL checkpoint time", Units: "milliseconds", Priority: 46050,
			Dimensions: []*registry.Dimension{{ID: "write", Algorithm: inc}, {ID: "sync", Algorithm: inc}}},
		{ID: "postgres.locks", Title: "PostgreSQL locks", Units: "locks", Priority: 46060,
			Dimensions: []*registry.Dimension{{ID: "held"}}},
	} {
		c.Family, c.Plugin, c.Module = "postgres", "postgres", "postgres"
		reg.AddChart(c)
	}
	return nil
}

type pgStats struct {
	used, idle, committed, rolled, fetched, returned, inserted, updated, deleted, size float64
	bufCkpt, bufBackend, bufClean, bufAlloc, ckptWrite, ckptSync, locks                float64
}

func (p *postgresCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := p.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("postgres.connections", now, map[string]float64{"used": s.used, "idle": s.idle})
	_ = reg.Collect("postgres.db_transactions", now, map[string]float64{"committed": s.committed, "rolled_back": s.rolled})
	_ = reg.Collect("postgres.db_tuples", now, map[string]float64{
		"fetched": s.fetched, "returned": s.returned, "inserted": s.inserted, "updated": s.updated, "deleted": s.deleted,
	})
	_ = reg.Collect("postgres.db_size", now, map[string]float64{"size": s.size})
	_ = reg.Collect("postgres.bgwriter_buffers", now, map[string]float64{
		"checkpoint": s.bufCkpt, "backend": s.bufBackend, "bgwriter": s.bufClean, "allocated": s.bufAlloc,
	})
	_ = reg.Collect("postgres.bgwriter_checkpoint_time", now, map[string]float64{"write": s.ckptWrite, "sync": s.ckptSync})
	_ = reg.Collect("postgres.locks", now, map[string]float64{"held": s.locks})
	return nil
}

func (p *postgresCollector) stats(ctx context.Context) (pgStats, error) {
	addrs := []string{p.cfg.Address}
	if p.cfg.Address == "127.0.0.1:5432" {
		addrs = append(addrs, "unix:///var/run/postgresql", "unix:///tmp")
	}
	q := `SELECT
  (SELECT count(*) FROM pg_stat_activity) AS used,
  (SELECT count(*) FROM pg_stat_activity WHERE state = 'idle') AS idle,
  (SELECT COALESCE(sum(xact_commit),0) FROM pg_stat_database) AS committed,
  (SELECT COALESCE(sum(xact_rollback),0) FROM pg_stat_database) AS rolled,
  (SELECT COALESCE(sum(tup_fetched),0) FROM pg_stat_database) AS fetched,
  (SELECT COALESCE(sum(tup_returned),0) FROM pg_stat_database) AS returned,
  (SELECT COALESCE(sum(tup_inserted),0) FROM pg_stat_database) AS inserted,
  (SELECT COALESCE(sum(tup_updated),0) FROM pg_stat_database) AS updated,
  (SELECT COALESCE(sum(tup_deleted),0) FROM pg_stat_database) AS deleted,
  (SELECT COALESCE(sum(pg_database_size(datname)),0) FROM pg_database) AS size,
  (SELECT buffers_checkpoint FROM pg_stat_bgwriter) AS buf_ckpt,
  (SELECT buffers_backend FROM pg_stat_bgwriter) AS buf_backend,
  (SELECT buffers_clean FROM pg_stat_bgwriter) AS buf_clean,
  (SELECT buffers_alloc FROM pg_stat_bgwriter) AS buf_alloc,
  (SELECT checkpoint_write_time FROM pg_stat_bgwriter) AS ckpt_write,
  (SELECT checkpoint_sync_time FROM pg_stat_bgwriter) AS ckpt_sync,
  (SELECT count(*) FROM pg_locks) AS locks;`
	var last error
	for _, a := range addrs {
		rows, err := pgQuery(ctx, a, p.cfg.User, p.cfg.Password, p.cfg.Database, p.cfg.Timeout, q)
		if err != nil {
			last = err
			if !isConnRefused(err) {
				return pgStats{}, err
			}
			continue
		}
		if len(rows) == 0 || len(rows[0]) < 17 {
			return pgStats{}, fmt.Errorf("postgres: unexpected row")
		}
		f := func(i int) float64 { v, _ := strconv.ParseFloat(rows[0][i], 64); return v }
		return pgStats{
			used: f(0), idle: f(1), committed: f(2), rolled: f(3), fetched: f(4), returned: f(5),
			inserted: f(6), updated: f(7), deleted: f(8), size: f(9),
			bufCkpt: f(10), bufBackend: f(11), bufClean: f(12), bufAlloc: f(13),
			ckptWrite: f(14), ckptSync: f(15), locks: f(16),
		}, nil
	}
	return pgStats{}, last
}

func pgQuery(ctx context.Context, address, user, password, database string, timeout time.Duration, sql string) ([][]string, error) {
	d := net.Dialer{Timeout: timeout}
	network, addr := "tcp", address
	if strings.HasPrefix(address, "unix://") {
		network = "unix"
		dir := strings.TrimPrefix(address, "unix://")
		if strings.Contains(dir, ".s.PGSQL") {
			addr = dir
		} else {
			addr = strings.TrimRight(dir, "/") + "/.s.PGSQL.5432"
		}
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

	if err := pgWriteStartup(conn, user, database); err != nil {
		return nil, err
	}
	if err := pgAuth(conn, user, password); err != nil {
		return nil, err
	}
	if err := pgWriteMsg(conn, 'Q', append([]byte(sql), 0)); err != nil {
		return nil, err
	}
	rows, _, err := pgReadRows(conn)
	return rows, err
}

func pgQueryWithCols(ctx context.Context, address, user, password, database string, timeout time.Duration, sql string) ([][]string, []string, error) {
	d := net.Dialer{Timeout: timeout}
	network, addr := "tcp", address
	if strings.HasPrefix(address, "unix://") {
		network = "unix"
		dir := strings.TrimPrefix(address, "unix://")
		if strings.Contains(dir, ".s.PGSQL") {
			addr = dir
		} else {
			addr = strings.TrimRight(dir, "/") + "/.s.PGSQL.5432"
		}
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if err := pgWriteStartup(conn, user, database); err != nil {
		return nil, nil, err
	}
	if err := pgAuth(conn, user, password); err != nil {
		return nil, nil, err
	}
	if err := pgWriteMsg(conn, 'Q', append([]byte(sql), 0)); err != nil {
		return nil, nil, err
	}
	return pgReadRows(conn)
}

func pgWriteStartup(w io.Writer, user, database string) error {
	var payload []byte
	payload = binary.BigEndian.AppendUint32(payload, 196608) // 3.0
	payload = append(payload, []byte("user\x00"+user+"\x00database\x00"+database+"\x00\x00")...)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(4+len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func pgWriteMsg(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(4+len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func pgReadMsg(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[1:])) - 4
	if n < 0 || n > 16<<20 {
		return 0, nil, fmt.Errorf("postgres: bad message length")
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], buf, nil
}

func pgAuth(conn net.Conn, user, password string) error {
	for {
		typ, payload, err := pgReadMsg(conn)
		if err != nil {
			return err
		}
		switch typ {
		case 'R': // Authentication
			if len(payload) < 4 {
				return fmt.Errorf("postgres: truncated auth")
			}
			method := binary.BigEndian.Uint32(payload)
			switch method {
			case 0: // OK
				continue
			case 3: // cleartext
				if err := pgWriteMsg(conn, 'p', append([]byte(password), 0)); err != nil {
					return err
				}
			case 5: // MD5
				if len(payload) < 8 {
					return fmt.Errorf("postgres: truncated md5 salt")
				}
				salt := payload[4:8]
				if err := pgWriteMsg(conn, 'p', append([]byte(pgMD5(user, password, salt)), 0)); err != nil {
					return err
				}
			default:
				return fmt.Errorf("postgres: unsupported auth method %d (need trust/password/md5)", method)
			}
		case 'E':
			return fmt.Errorf("postgres: %s", pgErr(payload))
		case 'S', 'K', 'N': // ParameterStatus, BackendKeyData, Notice
			continue
		case 'Z': // ReadyForQuery
			return nil
		default:
			continue
		}
	}
}

func pgMD5(user, password string, salt []byte) string {
	inner := md5.Sum([]byte(password + user))
	outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), salt...))
	return "md5" + hex.EncodeToString(outer[:])
}

func pgReadRows(r io.Reader) ([][]string, []string, error) {
	var ncols int
	var cols []string
	var rows [][]string
	for {
		typ, payload, err := pgReadMsg(r)
		if err != nil {
			return nil, nil, err
		}
		switch typ {
		case 'T': // RowDescription
			if len(payload) >= 2 {
				ncols = int(binary.BigEndian.Uint16(payload[0:2]))
			}
			cols = pgParseColNames(payload)
		case 'D': // DataRow
			if len(payload) < 2 {
				continue
			}
			n := int(binary.BigEndian.Uint16(payload[0:2]))
			if ncols == 0 {
				ncols = n
			}
			row := make([]string, n)
			i := 2
			for c := 0; c < n && i+4 <= len(payload); c++ {
				ln := int(int32(binary.BigEndian.Uint32(payload[i:])))
				i += 4
				if ln < 0 {
					row[c] = ""
					continue
				}
				if i+ln > len(payload) {
					return nil, nil, fmt.Errorf("postgres: truncated row")
				}
				row[c] = string(payload[i : i+ln])
				i += ln
			}
			rows = append(rows, row)
		case 'C', 'N', 'S', 'K':
			continue
		case 'E':
			return nil, nil, fmt.Errorf("postgres: %s", pgErr(payload))
		case 'Z':
			return rows, cols, nil
		}
	}
}

func pgParseColNames(payload []byte) []string {
	if len(payload) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(payload[0:2]))
	i := 2
	out := make([]string, 0, n)
	for c := 0; c < n && i < len(payload); c++ {
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		out = append(out, string(payload[i:end]))
		i = end + 1 + 4 + 2 + 4 + 2 + 4 + 2 // skip tableoid, attr, typoid, typlen, typmod, format
	}
	return out
}

func pgErr(b []byte) string {
	// fields: byte code, cstring, ...
	parts := strings.Split(string(b), "\x00")
	for _, p := range parts {
		if len(p) > 1 && (p[0] == 'M' || p[0] == 'C') {
			return p[1:]
		}
	}
	return strings.TrimRight(string(b), "\x00")
}
