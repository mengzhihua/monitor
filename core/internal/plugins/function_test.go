package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPackageFunctionDeclarationAndSwitch(t *testing.T) {
	spec := Spec{Name: "example", Manifest: &Manifest{ID: "example", Capabilities: []string{"metrics", "functions"}}}
	enabled := New(newReg(&memSink{}), nil, Options{}).parserFor(spec)
	if !enabled.AllowsFunctions {
		t.Fatal("declared functions stayed disabled")
	}
	if err := enabled.Line(`FUNCTION ping 5 "say ping"`); err != nil {
		t.Fatal(err)
	}
	if got := enabled.Functions(); len(got) != 1 || got[0] != "plugin.example.ping" {
		t.Fatalf("functions=%v", got)
	}
	if err := enabled.Line("FUNCTION NOT_A_NAME 5 nope"); err == nil {
		t.Fatal("accepted an unsafe function name")
	}
	if err := enabled.Line("FUNCTION other 90 too-slow"); err == nil {
		t.Fatal("accepted a timeout above 60 seconds")
	}
	off := false
	disabled := New(newReg(&memSink{}), nil, Options{Functions: &off}).parserFor(spec)
	if disabled.AllowsFunctions {
		t.Fatal("functions: false left calls enabled")
	}
	if err := disabled.Line("FUNCTION ping 5 ping"); err == nil {
		t.Fatal("disabled package accepted FUNCTION")
	}
}

func TestFunctionResultIsDeliveredOnce(t *testing.T) {
	p := &Parser{Plugin: "example", AllowsFunctions: true, Namespace: "plugin.example.", MaxLine: 64 << 10}
	ch := p.beginCall("abc123")
	if err := p.Line(`FUNCTION_RESULT abc123 {"ok":true,"n":2}`); err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.err != nil || got.body != `{"ok":true,"n":2}` {
		t.Fatalf("%+v", got)
	}
	big := strings.Repeat("x", p.maxResult()+8)
	ch = p.beginCall("abc124")
	if err := p.Line("FUNCTION_RESULT abc124 " + big); err == nil {
		t.Fatal("oversized result accepted")
	}
	got = <-ch
	if got.err == nil {
		t.Fatal("caller was not told the result was too large")
	}
}

func TestPluginFunctionCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fn.plugin")
	body := "#!/bin/sh\nprintf 'FUNCTION ping 5 ping\\n'\nwhile IFS=' ' read -r cmd id name args; do\n  printf 'FUNCTION_RESULT %s {\"ok\":true}\\n' \"$id\"\ndone\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "example", Command: script, UpdateEvery: 1, Timeout: 30, Manifest: &Manifest{ID: "example", Capabilities: []string{"metrics", "functions"}}}
	m := New(newReg(&memSink{}), []Spec{spec}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	var fn FunctionSpec
	for time.Now().Before(deadline) {
		for _, item := range m.Functions() {
			if item.Name == "plugin.example.ping" {
				fn = item
			}
		}
		if fn.Run != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fn.Run == nil {
		t.Fatal("plugin function was not registered")
	}
	callCtx, cancelCall := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCall()
	result, err := fn.Run(callCtx, map[string]string{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := result.(map[string]any)
	if !ok || payload["ok"] != true {
		t.Fatalf("result=%#v", result)
	}
	if _, err := fn.Run(callCtx, map[string]string{"bad key": "x"}); err == nil {
		t.Fatal("accepted an invalid argument name")
	}
}

func TestManifestAcceptsFunctionsCapability(t *testing.T) {
	for _, caps := range [][]string{{"metrics", "functions"}, {"functions"}} {
		dir := t.TempDir()
		packageFixture(t, dir, "example", func(m map[string]any) { m["capabilities"] = caps })
		specs, diagnostics := DiscoverManifests(dir)
		if len(diagnostics) != 0 || len(specs) != 1 || !specs[0].Manifest.allowsFunctions() {
			t.Fatalf("caps=%v specs=%d diagnostics=%v", caps, len(specs), diagnostics)
		}
	}
	dir := t.TempDir()
	packageFixture(t, dir, "example", func(m map[string]any) { m["capabilities"] = []string{"metrics", "metrics"} })
	if specs, diagnostics := DiscoverManifests(dir); len(specs) != 0 || len(diagnostics) != 1 {
		t.Fatalf("duplicate capability accepted: %+v %+v", specs, diagnostics)
	}
}

func TestCallArgsRejectTooMany(t *testing.T) {
	args := map[string]string{}
	for i := range maxFunctionArgs + 1 {
		args[string(rune('a'+i))] = "v"
	}
	if err := validateCallArgs(args); err == nil {
		t.Fatal("accepted too many arguments")
	}
	if err := validateCallArgs(map[string]string{"ok": "v"}); err != nil {
		t.Fatal(err)
	}
}
