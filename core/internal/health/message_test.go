package health

import "testing"

func TestRenderMessageUsesOnlyKnownTokens(t *testing.T) {
	entry := LogEntry{Hostname: "web\r\nBcc: evil@x", Name: "disk_full", Chart: "disk.space", Status: StatusCritical, Value: 95, Units: "%", Info: "full"}
	got, err := RenderMessage("[{HOST.NAME}] {EVENT.NAME} on {EVENT.CHART} is {EVENT.STATUS} {EVENT.VALUE}{EVENT.UNITS}: {EVENT.INFO}", entry)
	if err != nil {
		t.Fatal(err)
	}
	if got != "[web\r\nBcc: evil@x] disk_full on disk.space is CRITICAL 95.00%: full" && got != "[web\r\nBcc: evil@x] disk_full on disk.space is CRITICAL 95%: full" {
		t.Fatalf("%q", got)
	}
	safe := headerSafe(got)
	if stringsContainsHeader(safe) {
		t.Fatalf("header injection survived: %q", safe)
	}
	if _, err := RenderMessage("{EVENT.SECRET}", entry); err == nil {
		t.Fatal("accepted an unknown token")
	}
	if _, err := RenderMessage("{EVENT.NAME", entry); err == nil {
		t.Fatal("accepted an unclosed token")
	}
}

func stringsContainsHeader(s string) bool {
	return len(s) > 0 && (containsCRLF(s))
}

func containsCRLF(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' || s[i] == '\n' {
			return true
		}
	}
	return false
}
