package preprocess

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxWalkBytes  = 256 * 1024
	maxWalkLines  = 4096
	maxWalkRows   = 1024
	maxWalkFields = 8
)

func applySNMPWalkJSON(st Step, text string) (string, error) {
	if len(text) > maxWalkBytes {
		return "", fmt.Errorf("snmp_walk_to_json: input too large")
	}
	fields, err := normalizeWalkFields(st.Fields)
	if err != nil {
		return "", err
	}
	rows := map[string]map[string]string{}
	var order []string
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		lines++
		if lines > maxWalkLines {
			return "", fmt.Errorf("snmp_walk_to_json: input too large")
		}
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		oid, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		oid = normalizeOID(oid)
		if !numericOID(oid) {
			continue
		}
		value, ok := snmpWalkValue(strings.TrimSpace(raw))
		if !ok {
			continue
		}
		for _, field := range fields {
			index, ok := walkIndex(oid, field.OID)
			if !ok {
				continue
			}
			row, exists := rows[index]
			if !exists {
				if len(rows) >= maxWalkRows {
					return "", fmt.Errorf("snmp_walk_to_json: too many rows")
				}
				row = map[string]string{"{#SNMPINDEX}": index}
				rows[index] = row
				order = append(order, index)
			}
			row[field.Name] = value
		}
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("snmp_walk_to_json: no match")
	}
	sort.Slice(order, func(i, j int) bool { return compareIndex(order[i], order[j]) < 0 })
	out := make([]map[string]string, 0, len(order))
	for _, index := range order {
		out = append(out, rows[index])
	}
	body, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func normalizeWalkFields(fields []WalkField) ([]WalkField, error) {
	if len(fields) == 0 || len(fields) > maxWalkFields {
		return nil, fmt.Errorf("snmp_walk_to_json: need 1 to 8 fields")
	}
	seenName := map[string]bool{}
	seenOID := map[string]bool{}
	out := make([]WalkField, len(fields))
	for i, field := range fields {
		name := strings.TrimSpace(field.Name)
		oid := normalizeOID(field.OID)
		if !walkMacro(name) || name == "{#SNMPINDEX}" {
			return nil, fmt.Errorf("snmp_walk_to_json: field name must be a {#MACRO}")
		}
		if !numericOID(oid) || len(oid) > 128 {
			return nil, fmt.Errorf("snmp_walk_to_json: field %s needs a numeric OID", name)
		}
		if seenName[name] || seenOID[oid] {
			return nil, fmt.Errorf("snmp_walk_to_json: duplicate field %s", name)
		}
		seenName[name] = true
		seenOID[oid] = true
		out[i] = WalkField{Name: name, OID: oid}
	}
	return out, nil
}

func walkMacro(name string) bool {
	if len(name) < 4 || !strings.HasPrefix(name, "{#") || !strings.HasSuffix(name, "}") {
		return false
	}
	body := name[2 : len(name)-1]
	if body == "" || len(body) > 32 {
		return false
	}
	for _, r := range body {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func normalizeOID(oid string) string {
	oid = strings.TrimSpace(oid)
	return strings.Trim(oid, ".")
}

func numericOID(oid string) bool {
	if oid == "" {
		return false
	}
	for _, part := range strings.Split(oid, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func walkIndex(oid, prefix string) (string, bool) {
	if oid == prefix {
		return "0", true
	}
	if strings.HasPrefix(oid, prefix+".") {
		return oid[len(prefix)+1:], true
	}
	return "", false
}

func snmpWalkValue(raw string) (string, bool) {
	if raw == "" || strings.HasPrefix(raw, "No Such") {
		return "", false
	}
	typ, val, ok := strings.Cut(raw, ":")
	if !ok {
		return strings.Trim(strings.TrimSpace(raw), `"`), true
	}
	val = strings.TrimSpace(val)
	if val == "" || strings.HasPrefix(val, "No Such") {
		return "", false
	}
	switch strings.ToUpper(strings.TrimSpace(typ)) {
	case "INTEGER", "INTEGER32", "COUNTER32", "COUNTER64", "GAUGE32", "UNSIGNED32", "TIMETICKS":
		if n, ok := snmpNumber(val); ok {
			return n, true
		}
	}
	return strings.Trim(val, `"`), true
}

func snmpNumber(val string) (string, bool) {
	if i := strings.IndexByte(val, '('); i >= 0 {
		if j := strings.IndexByte(val[i+1:], ')'); j > 0 && numericOID(val[i+1:i+1+j]) && !strings.Contains(val[i+1:i+1+j], ".") {
			return val[i+1 : i+1+j], true
		}
	}
	token := val
	if space := strings.IndexAny(val, " \t"); space >= 0 {
		token = val[:space]
	}
	if _, err := strconv.ParseInt(token, 10, 64); err == nil {
		return token, true
	}
	return "", false
}

func compareIndex(a, b string) int {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	n := len(ap)
	if len(bp) < n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		ai, aerr := strconv.Atoi(ap[i])
		bi, berr := strconv.Atoi(bp[i])
		if aerr != nil || berr != nil {
			if ap[i] != bp[i] {
				return strings.Compare(ap[i], bp[i])
			}
			continue
		}
		if ai != bi {
			return ai - bi
		}
	}
	return len(ap) - len(bp)
}
