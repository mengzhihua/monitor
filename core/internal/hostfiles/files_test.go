package hostfiles

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUploadPublicationAndCancellation(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Dir: dir}
	path := "document.bin"
	start := func() string {
		t.Helper()
		data, err := store.Run("upload-start", map[string]string{"path": path, "size": strconv.Itoa(MaxBytes + 1)})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := start()
	run := func(op string, args map[string]string) error {
		t.Helper()
		args["path"] = path
		args["id"] = id
		_, err := store.Run(op, args)
		return err
	}
	if err := run("upload-finish", map[string]string{}); err == nil {
		t.Fatal("published incomplete upload")
	}
	if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
		t.Fatal("incomplete target is visible")
	}
	if err := run("upload-cancel", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging cleanup: %v %v", entries, err)
	}
	id = start()
	if err := run("upload-chunk", map[string]string{"offset": "0", "data": base64.StdEncoding.EncodeToString(make([]byte, MaxBytes))}); err != nil {
		t.Fatal(err)
	}
	if err := run("upload-chunk", map[string]string{"offset": "0", "data": "YQ=="}); err == nil {
		t.Fatal("accepted replayed offset")
	}
	if err := run("upload-chunk", map[string]string{"offset": strconv.Itoa(MaxBytes), "data": "YQ=="}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, path)
	if err := os.WriteFile(target, []byte("external writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("upload-finish", map[string]string{}); err == nil {
		t.Fatal("overwrote competing file")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "external writer" {
		t.Fatal("concurrent file changed")
	}
}
