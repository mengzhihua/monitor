package registry

import "testing"

func TestLabelsSnapshotIsDetached(t *testing.T) {
	var absent *Chart
	if absent.LabelsSnapshot() != nil || (&Chart{}).LabelsSnapshot() != nil {
		t.Fatal("empty chart must have no labels")
	}
	chart := &Chart{Labels: map[string]string{"region": "east"}}
	before := chart.LabelsSnapshot()
	chart.MergeLabels(map[string]string{"region": "west", "zone": "one"})
	if before["region"] != "east" || before["zone"] != "" {
		t.Fatal("snapshot changed after a label merge")
	}
	before["region"] = "caller"
	if chart.LabelsSnapshot()["region"] != "west" {
		t.Fatal("caller changed chart labels through a snapshot")
	}
}
