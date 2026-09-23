package discover

import "testing"

func TestMDNSQueryRoundTrip(t *testing.T) {
	q := Query()
	if !wantsService(q) {
		t.Fatal("query should ask for _monitor._tcp.local")
	}
	pkt := response("box", 19999, []byte{10, 1, 2, 3})
	url, ok := ParseResponse(pkt)
	if !ok || url != "http://10.1.2.3:19999" {
		t.Fatalf("%s %v", url, ok)
	}
}

func TestPort(t *testing.T) {
	if Port(":19999") != 19999 || Port("127.0.0.1:80") != 80 {
		t.Fatal(Port(":19999"), Port("127.0.0.1:80"))
	}
}
