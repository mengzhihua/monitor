package sla

import "testing"

func TestCatalogRejectsCyclesAndPersistsEdits(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/service-catalog.json"
	catalog, err := OpenCatalog(path, []Service{{Name: "edge", Alarms: []string{"down"}}}, []Link{{Source: "a", Target: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ReplaceServices([]Service{
		{Name: "edge", Children: []string{"core"}},
		{Name: "core", Children: []string{"edge"}},
	}); err == nil {
		t.Fatal("accepted a service cycle")
	}
	if _, err := catalog.ReplaceServices([]Service{{Name: "edge", Children: []string{"missing"}}}); err == nil {
		t.Fatal("accepted a missing child")
	}
	services, err := catalog.ReplaceServices([]Service{
		{Name: "edge", Children: []string{"core"}},
		{Name: "core", Alarms: []string{"down"}},
	})
	if err != nil || len(services) != 2 {
		t.Fatalf("%+v %v", services, err)
	}
	links, err := catalog.ReplaceLinks([]Link{{Source: "core", Target: "edge"}, {Source: "core", Target: "edge"}})
	if err != nil || len(links) != 1 || links[0].Source != "core" {
		t.Fatalf("%+v %v", links, err)
	}
	reloaded, err := OpenCatalog(path, []Service{{Name: "seed"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Services()
	if len(got) != 2 || got[0].Name != "edge" || len(got[0].Children) != 1 || got[1].Name != "core" || len(got[1].Children) != 0 {
		t.Fatalf("saved catalog did not replace the seed: %+v", got)
	}
	if got := reloaded.Links(); len(got) != 1 || got[0].Target != "edge" {
		t.Fatalf("%+v", got)
	}
}
