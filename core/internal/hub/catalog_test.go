package hub

import (
	"testing"
	"time"
)

func TestHostGroupTreeAndPermissions(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := o.CreateHostGroup("linux", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := o.CreateHostGroup("web", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateHostGroup(parent.ID, "linux", child.ID, nil); err == nil {
		t.Fatal("parent linked to its child")
	}
	if _, err := o.UpdateHostGroup(child.ID, "web", child.ID, []string{"n1"}); err == nil {
		t.Fatal("group parented to itself")
	}
	updated, err := o.UpdateHostGroup(child.ID, "web", parent.ID, []string{"n1", "n1"})
	if err != nil || len(updated.Nodes) != 1 {
		t.Fatalf("%+v %v", updated, err)
	}
	if !o.NodeInHostGroups("n1", []string{parent.ID}) {
		t.Fatal("parent grant missed a child node")
	}
	if o.NodeInHostGroups("n2", []string{parent.ID}) {
		t.Fatal("unlisted node matched")
	}
	if err := o.DeleteHostGroup(parent.ID); err == nil {
		t.Fatal("deleted a group that still has a child")
	}
	if err := o.DeleteHostGroup(child.ID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.HostGroups()) != 1 || reloaded.HostGroups()[0].Name != "linux" {
		t.Fatalf("%+v", reloaded.HostGroups())
	}
}

func TestProxyAssignmentAndRoute(t *testing.T) {
	o, err := OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0)
	active, err := o.CreateProxy("edge", "http://proxy.example:19999", "active", []string{"n1"})
	if err != nil || active.Token == "" {
		t.Fatalf("%+v %v", active, err)
	}
	if _, ok := o.ProxyRoute("n1", now); ok {
		t.Fatal("active proxy routed before a heartbeat")
	}
	if err := o.TouchProxy(active.ID, "nope", now); err == nil {
		t.Fatal("bad token heartbeated")
	}
	if err := o.TouchProxy(active.ID, active.Token, now); err != nil {
		t.Fatal(err)
	}
	if url, ok := o.ProxyRoute("n1", now.Add(time.Minute)); !ok || url != "http://proxy.example:19999" {
		t.Fatalf("route %q %v", url, ok)
	}
	if _, ok := o.ProxyRoute("n1", now.Add(3*time.Minute)); ok {
		t.Fatal("stale active proxy still routed")
	}
	passive, err := o.CreateProxy("lab", "https://lab.example", "passive", []string{"n1", "n2"})
	if err != nil {
		t.Fatal(err)
	}
	if url, ok := o.ProxyRoute("n1", now); !ok || url != "https://lab.example" {
		t.Fatalf("node stayed on the active proxy: %q %v", url, ok)
	}
	listed := o.Proxies()
	for _, p := range listed {
		if p.Token != "" {
			t.Fatal("proxy token listed")
		}
	}
	if err := o.DeleteProxy(passive.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := o.ProxyRoute("n2", now); ok {
		t.Fatal("deleted proxy still routed")
	}
}
