package collect

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestSSHAndTelnetFailuresMarkUnsupported(t *testing.T) {
	reg := testReg(t)
	ssh := &sshcheckCollector{cfg: sshcheckConfig{Command: "ssh", Timeout: time.Second, Jobs: []sshJob{{
		Name: "load", Address: "10.0.0.5", User: "ops", Command: "uptime",
	}}}}
	ssh.run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("connection refused")
	}
	if err := ssh.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ssh.Collect(context.Background(), reg, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	sshChart := mustChart(t, reg, "sshcheck.load")
	if sshChart.Unsupported == "" || sshChart.LastCollectionTime() != 0 {
		t.Fatalf("ssh unsupported %q at %d", sshChart.Unsupported, sshChart.LastCollectionTime())
	}
	ssh.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("4\n"), nil
	}
	if err := ssh.Collect(context.Background(), reg, time.Unix(11, 0)); err != nil {
		t.Fatal(err)
	}
	if msg := sshChart.Unsupported; msg != "" {
		t.Fatalf("ssh error stuck: %q", msg)
	}
	if _, v := sshChart.LastValues(); v["value"] != 4 {
		t.Fatalf("ssh value %+v", v)
	}

	tel := &telnetCollector{cfg: telnetConfig{Timeout: time.Second, Jobs: []telnetJob{{
		Name: "banner", Address: "127.0.0.1:1",
	}}}}
	tel.dial = func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	if err := tel.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := tel.Collect(context.Background(), reg, time.Unix(12, 0)); err != nil {
		t.Fatal(err)
	}
	telChart := mustChart(t, reg, "telnet.banner")
	if telChart.Unsupported == "" || telChart.LastCollectionTime() != 0 {
		t.Fatalf("telnet unsupported %q at %d", telChart.Unsupported, telChart.LastCollectionTime())
	}
}
