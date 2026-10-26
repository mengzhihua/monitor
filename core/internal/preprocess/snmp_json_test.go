package preprocess

import (
	"encoding/json"
	"testing"
)

func TestSNMPWalkToJSON(t *testing.T) {
	walk := "" +
		".1.3.6.1.2.1.2.2.1.2.1 = STRING: \"lo\"\n" +
		".1.3.6.1.2.1.2.2.1.2.2 = STRING: \"eth0\"\n" +
		".1.3.6.1.2.1.2.2.1.8.1 = INTEGER: up(1)\n" +
		".1.3.6.1.2.1.2.2.1.8.2 = INTEGER: 2\n" +
		".1.3.6.1.2.1.1.3.0 = Timeticks: (123) 0:00:01.23\n" +
		".1.3.6.1.2.1.2.2.1.2.3 = No Such Instance currently exists at this OID\n"
	step := Step{Type: "snmp_walk_to_json", Fields: []WalkField{
		{Name: "{#IFNAME}", OID: "1.3.6.1.2.1.2.2.1.2"},
		{Name: "{#IFSTATUS}", OID: ".1.3.6.1.2.1.2.2.1.8"},
	}}
	raw, err := applySNMPWalkJSON(step, walk)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["{#SNMPINDEX}"] != "1" || rows[0]["{#IFNAME}"] != "lo" || rows[0]["{#IFSTATUS}"] != "1" {
		t.Fatalf("first %+v", rows)
	}
	if rows[1]["{#IFNAME}"] != "eth0" || rows[1]["{#IFSTATUS}"] != "2" {
		t.Fatalf("second %+v", rows[1])
	}
	ticks, err := applySNMPWalkJSON(Step{Type: "snmp_walk_to_json", Fields: []WalkField{{Name: "{#UPTIME}", OID: "1.3.6.1.2.1.1.3"}}}, walk)
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal([]byte(ticks), &rows); err != nil || rows[0]["{#UPTIME}"] != "123" || rows[0]["{#SNMPINDEX}"] != "0" {
		t.Fatalf("timeticks %+v %v", rows, err)
	}
	if _, err := applySNMPWalkJSON(step, "no walk here\n"); err == nil {
		t.Fatal("accepted a walk with no matches")
	}
	if _, err := applySNMPWalkJSON(Step{Type: "snmp_walk_to_json"}, walk); err == nil {
		t.Fatal("accepted a step without fields")
	}
	if _, err := applySNMPWalkJSON(Step{Type: "snmp_walk_to_json", Fields: []WalkField{{Name: "ifname", OID: "1.3.6.1.2.1.2.2.1.2"}}}, walk); err == nil {
		t.Fatal("accepted a field name that is not a macro")
	}
}
