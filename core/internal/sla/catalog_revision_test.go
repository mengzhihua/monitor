package sla

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestCatalogRevisionRulesAndConcurrentWriters(t *testing.T) {
	path := t.TempDir() + "/catalog.json"
	c, err := OpenCatalog(path, []Service{{Name: "parent", Children: []string{"child"}, StatusRules: []StatusRule{{Type: "child", Child: "child", Status: "WARNING"}}}, {Name: "child"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := c.ServiceSnapshot()
	original.Services[0].StatusRules[0].Status = "CRITICAL"
	if c.Services()[0].StatusRules[0].Status != "WARNING" {
		t.Fatal("snapshot aliases rules")
	}
	preview, err := c.ChangeServices(original.Revision, original.Services, true)
	if err != nil || preview.Services[0].StatusRules[0].Status != "CRITICAL" {
		t.Fatalf("%+v %v", preview, err)
	}
	if c.ServiceSnapshot().Revision != original.Revision {
		t.Fatal("preview mutated catalog")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote disk")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := c.ChangeServices(original.Revision, []Service{{Name: name}}, false)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	saved := c.ServiceSnapshot()
	reloaded, err := OpenCatalog(path, nil, nil)
	if err != nil || reloaded.ServiceSnapshot().Revision != saved.Revision {
		t.Fatalf("restart revision: %v", err)
	}
	if _, err := c.ChangeServices(saved.Revision, original.Services, false); err != nil {
		t.Fatal(err)
	}
	reloaded, err = OpenCatalog(path, nil, nil)
	if err != nil || reloaded.Services()[0].StatusRules[0].Status != "CRITICAL" {
		t.Fatal("rules lost on restart")
	}
	bad := reloaded.Services()
	bad[0].StatusRules[0].Type = "invalid"
	if _, err := reloaded.ChangeServices(reloaded.ServiceSnapshot().Revision, bad, false); err == nil {
		t.Fatal("invalid rules accepted")
	}
}
func TestCatalogSaveFailurePreservesRevision(t *testing.T) {
	dir := t.TempDir()
	c, _ := OpenCatalog(dir+"/child/catalog.json", []Service{{Name: "old"}}, nil)
	before := c.ServiceSnapshot()
	if err := os.WriteFile(dir+"/child", []byte("block directory creation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeServices(before.Revision, []Service{{Name: "new"}}, false); err == nil {
		t.Fatal("save should fail")
	}
	if c.ServiceSnapshot().Revision != before.Revision {
		t.Fatal("failed save mutated memory")
	}
}
