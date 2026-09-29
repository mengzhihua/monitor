package collect

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
)

func TestTelnetReadsPromptedValue(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		_, _ = r.ReadString('\n')
		_, _ = c.Write([]byte("load=1.25\n"))
	}()
	col := &telnetCollector{cfg: telnetConfig{Timeout: time.Second, Jobs: []telnetJob{{
		Name: "load", Address: ln.Addr().String(), Send: "show",
		Steps: []preprocess.Step{{Type: "regex", Pattern: `load=([0-9.]+)`}},
	}}}}
	reg := testReg(t)
	if err := col.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := col.Collect(context.Background(), reg, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "telnet.load").LastValues(); v["value"] != 1.25 {
		t.Fatalf("%+v", v)
	}
}

func TestSNMPTopologyUsesV3Args(t *testing.T) {
	s := &snmpTopologyCollector{cfg: snmpTopologyConfig{
		Address: "10.0.0.9", Version: "3", User: "mon", AuthPass: "a", PrivPass: "p", Command: "snmpwalk", Timeout: time.Second,
	}}
	s.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !containsAll(args, []string{"-v", "3", "-l", "authPriv", "-u", "mon"}) {
			t.Fatalf("args %v", args)
		}
		return []byte(".1.0.8802.1.1.2.1.4.1.1.9.0.1 = STRING: edge-sw\n"), nil
	}
	reg := testReg(t)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("snmp_topology.lldp_neighbor.edge-sw"); !ok {
		t.Fatal("missing neighbor chart")
	}
}
