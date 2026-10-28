package hub

import (
	"testing"

	"github.com/mengzhihua/monitor/core/internal/stream"
)

func TestFunctionCatalogOnlyCurrentSessionCanReplace(t *testing.T) {
	nodes := &Nodes{}
	current, stale := &session{n: nodes}, &session{n: nodes}
	node := &Node{conn: current, functions: []stream.FunctionInfo{{Name: "initial"}}}
	frame := stream.Frame{Type: stream.TypeFunctions, Functions: []stream.FunctionInfo{{Name: "plugin.demo.ping"}}}
	stale.handle(node, frame)
	if got := node.Functions(); len(got) != 1 || got[0].Name != "initial" || nodes.gen != 0 {
		t.Fatalf("replaced session changed current catalog: %+v", got)
	}
	current.handle(node, frame)
	frame.Functions[0].Name = "mutated"
	if got := node.Functions(); len(got) != 1 || got[0].Name != "plugin.demo.ping" || nodes.gen != 1 {
		t.Fatalf("catalog not replaced with an owned snapshot: %+v", got)
	}
	current.handle(node, stream.Frame{Type: stream.TypeFunctions})
	if got := node.Functions(); len(got) != 0 || nodes.gen != 2 {
		t.Fatalf("empty snapshot did not remove functions: %+v", got)
	}
}
