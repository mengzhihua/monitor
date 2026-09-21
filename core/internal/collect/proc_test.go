package collect

import (
	"testing"
)

func TestParseProcStat(t *testing.T) {
	st := parseProcStat("cpu 1 2 3\nintr 12345 0 1\nctxt 9\nprocesses 77\n")
	if st.intr != 12345 || st.forks != 77 {
		t.Fatalf("%+v", st)
	}
}

func TestParseFileNR(t *testing.T) {
	a, u, m, err := parseFileNR("1234\t0\t922337")
	if err != nil || a != 1234 || u != 0 || m != 922337 {
		t.Fatalf("%v %v %v %v", a, u, m, err)
	}
	if _, _, _, err := parseFileNR("1 2"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePressure(t *testing.T) {
	p := parsePressure("some avg10=1.50 avg60=2.00 avg300=3.25 total=1000\nfull avg10=0.10 avg60=0.20 avg300=0.30 total=50\n")
	if p.some10 != 1.5 || p.some60 != 2 || p.fullTotal != 50 {
		t.Fatalf("%+v", p)
	}
}

func TestParseSNMP(t *testing.T) {
	raw := "Tcp: RtoAlgorithm RtoMin CurrEstab InSegs OutSegs RetransSegs\n" +
		"Tcp: 1 200 10 100 80 3\n" +
		"Udp: InDatagrams NoPorts InErrors OutDatagrams\n" +
		"Udp: 5 1 0 7\n"
	m := parseSNMP(raw)
	if snmpGet(m, "Tcp", "CurrEstab") != 10 || snmpGet(m, "Udp", "OutDatagrams") != 7 {
		t.Fatalf("%v", m)
	}
}

func TestParseApacheStatus(t *testing.T) {
	body := "Total Accesses: 42\nTotal kBytes: 8\nBusyWorkers: 3\nIdleWorkers: 7\n" +
		"ConnsTotal: 2\nConnsAsyncWriting: 0\nConnsAsyncKeepAlive: 1\nConnsAsyncClosing: 0\n" +
		"Scoreboard: ___W_K..\n"
	st, err := parseApacheStatus(body)
	if err != nil {
		t.Fatal(err)
	}
	if st.num["BusyWorkers"] != 3 || st.num["Total Accesses"] != 42 {
		t.Fatalf("%v", st.num)
	}
	sb := parseApacheScoreboard(st.scoreboard)
	if sb["waiting"] != 4 || sb["sending"] != 1 || sb["keepalive"] != 1 || sb["open"] != 2 {
		t.Fatalf("%v", sb)
	}
	if _, err := parseApacheStatus("not apache"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePHPFPMStatus(t *testing.T) {
	m, err := parsePHPFPMStatus("pool: www\naccepted conn: 10\nlisten queue: 1\nmax listen queue: 2\nidle processes: 3\nactive processes: 1\nslow requests: 4\n")
	if err != nil || m["accepted conn"] != 10 || m["active processes"] != 1 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestParseStatsDLine(t *testing.T) {
	g, c, tm := map[string]float64{}, map[string]float64{}, map[string][]float64{}
	parseStatsDLine(g, c, tm, "hits:2|c|@0.5")
	parseStatsDLine(g, c, tm, "temp:21|g")
	parseStatsDLine(g, c, tm, "temp:+1|g")
	parseStatsDLine(g, c, tm, "api:12|ms")
	if c["hits"] != 4 || g["temp"] != 22 || len(tm["api"]) != 1 {
		t.Fatalf("g=%v c=%v t=%v", g, c, tm)
	}
}

func TestRatePerOp(t *testing.T) {
	if ratePerOp(200, 100, 10, 5) != 20 {
		t.Fatal("rate")
	}
	if ratePerOp(200, 100, 5, 5) != 0 {
		t.Fatal("zero ops")
	}
}

func TestIcmpChecksum(t *testing.T) {
	pkt := icmpEcho(1, 1, []byte("hi"))
	// checksum field must make the whole packet sum to 0xffff
	if icmpChecksum(pkt) != 0 {
		t.Fatalf("checksum residual %x", icmpChecksum(pkt))
	}
}
