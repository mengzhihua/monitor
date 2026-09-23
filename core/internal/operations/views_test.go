package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func exampleView() SavedView {
	return SavedView{Name: "我的观察队列", Query: "db", Severity: "WARNING", NodeStatus: "live", PendingOnly: true, OwnerFilter: "mine", ProgressFilter: "watching"}
}

func TestViewsDurableIsolationAndDeletion(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenViews(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	views := []SavedView{exampleView()}
	c, err := s.Replace(key, 0, views)
	if err != nil || c.Revision != 1 {
		t.Fatal(c, err)
	}
	views[0].Query = "caller changed input"
	c.Views[0].Query = "caller changed output"
	if s.Get(key).Views[0].Query != "db" || len(s.Get(strings.Repeat("b", 64)).Views) != 0 {
		t.Fatal("isolation failed")
	}
	f, _ := os.Stat(filepath.Join(dir, "views.json"))
	if runtime.GOOS != "windows" && f.Mode().Perm() != 0600 {
		t.Fatal(f.Mode())
	}
	restarted, err := OpenViews(dir)
	if err != nil || !reflect.DeepEqual(s.Get(key), restarted.Get(key)) {
		t.Fatal(err)
	}
	if _, err := restarted.Replace(key, 1, []SavedView{}); err != nil {
		t.Fatal(err)
	}
	restarted, err = OpenViews(dir)
	if err != nil || restarted.Get(key).Revision != 2 || restarted.Get(key).Views == nil {
		t.Fatal(err)
	}
	if _, err := restarted.Replace(key, 0, []SavedView{exampleView()}); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted collection was resurrected", err)
	}
}

func TestViewsConcurrentWritersAndDiskFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenViews(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := s.Replace(key, 0, []SavedView{exampleView()})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	before := s.Get(key)
	// A directory at the destination forces rename failure even as root.
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(key, 1, []SavedView{}); err == nil {
		t.Fatal("write succeeded unexpectedly")
	}
	if !reflect.DeepEqual(s.Get(key), before) {
		t.Fatal("failed write published")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("temporary files leaked", files)
	}
}

func TestViewsValidationCapacityAndCorruption(t *testing.T) {
	s, _ := OpenViews("")
	key := strings.Repeat("a", 64)
	invalid := [][]SavedView{nil, {exampleView(), exampleView()}}
	for _, mutate := range []func(*SavedView){
		func(v *SavedView) { v.Name = " " }, func(v *SavedView) { v.Name = strings.Repeat("值", 41) },
		func(v *SavedView) { v.Query = strings.Repeat("x", 1025) }, func(v *SavedView) { v.Query = "a\nb" },
		func(v *SavedView) { v.Severity = "CLEAR" }, func(v *SavedView) { v.NodeStatus = "gone" },
		func(v *SavedView) { v.OwnerFilter = "someone" }, func(v *SavedView) { v.ProgressFilter = "resolved" },
	} {
		v := exampleView()
		mutate(&v)
		invalid = append(invalid, []SavedView{v})
	}
	tooMany := []SavedView{}
	for i := range 11 {
		v := exampleView()
		v.Name = fmt.Sprint(i)
		tooMany = append(tooMany, v)
	}
	invalid = append(invalid, tooMany)
	for _, vs := range invalid {
		if _, err := s.Replace(key, 0, vs); !errors.Is(err, ErrInvalidView) {
			t.Fatal(err)
		}
	}
	for i := range maxViewAccounts {
		s.state.Accounts[fmt.Sprintf("%064x", i)] = ViewCollection{Revision: 1, Views: []SavedView{}}
	}
	if _, err := s.Replace(key, 0, []SavedView{}); !errors.Is(err, ErrViewCapacity) {
		t.Fatal(err)
	}
	if _, err := s.Replace(fmt.Sprintf("%064x", 0), 1, []SavedView{exampleView()}); err != nil {
		t.Fatal("existing accounts must remain editable", err)
	}
	for _, bad := range []string{`{`, `{"version":2,"accounts":{}}`, `{"version":1,"accounts":null}`, `{"version":1,"accounts":{"bad":{"revision":1,"views":[]}}}`, `{"version":1,"accounts":{"` + key + `":{"revision":0,"views":[]}}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "views.json")
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenViews(dir); err == nil {
			t.Fatal("accepted damaged file", bad)
		}
		b, _ := os.ReadFile(path)
		if string(b) != bad {
			t.Fatal("damaged file overwritten")
		}
	}
}
