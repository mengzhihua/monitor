package hostfiles

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyMoveZipAndExtract(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Dir: dir}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "note.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("note.txt", filepath.Join(dir, "docs", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Run("copy", map[string]string{"path": "docs", "dest": "."}); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(dir, "docs 副本", "note.txt"))
	if err != nil || string(copied) != "hello" {
		t.Fatalf("copy: %q %v", copied, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "docs 副本", "link.txt")); !os.IsNotExist(err) {
		t.Fatal("symlink was copied")
	}
	if err = os.Mkdir(filepath.Join(dir, "box"), 0750); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Run("move", map[string]string{"path": "docs/note.txt", "dest": "box"}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "docs", "note.txt")); !os.IsNotExist(err) {
		t.Fatal("move left the source")
	}
	if _, err = store.Run("move", map[string]string{"path": "docs", "dest": "docs/inner"}); err == nil {
		t.Fatal("moved a directory into itself")
	}
	if _, err = store.Run("zip", map[string]string{"path": "box", "names": `["note.txt"]`, "name": "notes.zip"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Run("unzip", map[string]string{"path": "box/notes.zip", "dest": "."}); err != nil {
		t.Fatal(err)
	}
	extracted, err := os.ReadFile(filepath.Join(dir, "notes", "note.txt"))
	if err != nil || string(extracted) != "hello" {
		t.Fatalf("unzip: %q %v", extracted, err)
	}
	slip := filepath.Join(dir, "slip.zip")
	file, err := os.Create(slip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	writer, err := zw.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil || file.Close() != nil {
		t.Fatal(err)
	}
	if _, err = store.Run("unzip", map[string]string{"path": "slip.zip", "dest": "."}); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("zip slip: %v", err)
	}
}
