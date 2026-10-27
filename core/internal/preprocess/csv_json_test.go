package preprocess

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCSVToJSON(t *testing.T) {
	text := "name,value\n\"cpu,0\",12\ndisk,3\n"
	v, _, err := Apply([]Step{
		{Type: "csv_to_json", Header: true},
		{Type: "jsonpath", Path: "$[0].value"},
	}, text, math.NaN())
	if err != nil || v != 12 {
		t.Fatalf("first value %v %v", v, err)
	}
	v, _, err = Apply([]Step{
		{Type: "csv_to_json", Header: true},
		{Type: "jsonpath", Path: "$[1].value"},
	}, text, math.NaN())
	if err != nil || v != 3 {
		t.Fatalf("second value %v %v", v, err)
	}
	raw, err := applyCSVToJSON(Step{Type: "csv_to_json", Header: true}, "name,value\n\"a\"\"b\",1\n")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["name"] != `a"b` || rows[0]["value"] != "1" {
		t.Fatalf("quoted name %+v", rows)
	}
	v, _, err = Apply([]Step{
		{Type: "csv_to_json"},
		{Type: "jsonpath", Path: "$[0][1]"},
	}, "7,8\n", math.NaN())
	if err != nil || v != 8 {
		t.Fatalf("no header %v %v", v, err)
	}
	raw, err = applyCSVToJSON(Step{Type: "csv_to_json", Delimiter: ";", Header: true}, "name;value\ncpu;5\n")
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || rows[0]["value"] != "5" {
		t.Fatalf("semicolon %+v %v", rows, err)
	}
	if _, err = applyCSVToJSON(Step{Type: "csv_to_json", Header: true}, "name,value\nonly\n"); err == nil {
		t.Fatal("accepted a short row")
	}
	if _, err = applyCSVToJSON(Step{Type: "csv_to_json", Delimiter: ",,"}, "a,b\n"); err == nil {
		t.Fatal("accepted a long delimiter")
	}
	if _, err = applyCSVToJSON(Step{Type: "csv_to_json", Qualifier: ","}, "a,b\n"); err == nil {
		t.Fatal("accepted a qualifier equal to the delimiter")
	}
	if _, err = applyCSVToJSON(Step{Type: "csv_to_json"}, "\"a,b\n"); err == nil {
		t.Fatal("accepted an unclosed quote")
	}
	raw, err = applyCSVToJSON(Step{Type: "csv_to_json", NoQuote: true}, "\"a,b\",c\n")
	if err != nil {
		t.Fatal(err)
	}
	var plain [][]string
	if err := json.Unmarshal([]byte(raw), &plain); err != nil || len(plain) != 1 || len(plain[0]) != 3 || plain[0][0] != `"a` {
		t.Fatalf("no quote %+v %v", plain, err)
	}
}
