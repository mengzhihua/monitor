package preprocess

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxCSVBytes   = 256 * 1024
	maxCSVRows    = 256
	maxCSVColumns = 64
	maxCSVField   = 4096
)

func applyCSVToJSON(st Step, text string) (string, error) {
	if len(text) > maxCSVBytes {
		return "", fmt.Errorf("csv_to_json: input too large")
	}
	delim, qual, err := csvControls(st)
	if err != nil {
		return "", err
	}
	rows, err := parseCSVTable(text, delim, qual)
	if err != nil {
		return "", err
	}
	return csvAsJSON(rows, st.Header)
}

func csvControls(st Step) (string, string, error) {
	delim := st.Delimiter
	if delim == "" {
		delim = ","
	}
	if utf8.RuneCountInString(delim) != 1 || strings.ContainsAny(delim, "\r\n") {
		return "", "", fmt.Errorf("csv_to_json: delimiter must be one character")
	}
	qual := `"`
	if st.NoQuote {
		qual = ""
	} else if st.Qualifier != "" {
		qual = st.Qualifier
	}
	if qual != "" && (utf8.RuneCountInString(qual) != 1 || strings.ContainsAny(qual, "\r\n")) {
		return "", "", fmt.Errorf("csv_to_json: qualifier must be one character")
	}
	if qual != "" && qual == delim {
		return "", "", fmt.Errorf("csv_to_json: delimiter and qualifier must differ")
	}
	return delim, qual, nil
}

func parseCSVTable(text, delim, qual string) ([][]string, error) {
	if qual == "" {
		return parsePlainCSV(text, delim)
	}
	return parseQuotedCSV(text, []rune(delim)[0], []rune(qual)[0])
}

func parsePlainCSV(text, delim string) ([][]string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return [][]string{}, nil
	}
	lines := strings.Split(text, "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, delim)
		if len(fields) > maxCSVColumns {
			return nil, fmt.Errorf("csv_to_json: too many columns")
		}
		for _, field := range fields {
			if utf8.RuneCountInString(field) > maxCSVField {
				return nil, fmt.Errorf("csv_to_json: field too long")
			}
		}
		rows = append(rows, fields)
		if len(rows) > maxCSVRows {
			return nil, fmt.Errorf("csv_to_json: too many rows")
		}
	}
	return rows, nil
}

func parseQuotedCSV(text string, delim, qual rune) ([][]string, error) {
	var rows [][]string
	var row []string
	var field strings.Builder
	inQuote := false
	saw := false
	runes := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("csv_to_json: invalid text")
		}
		i += size
		if inQuote {
			if r == qual {
				if next, n := utf8.DecodeRuneInString(text[i:]); next == qual && n > 0 {
					if !writeCSVField(&field, &runes, qual) {
						return nil, fmt.Errorf("csv_to_json: field too long")
					}
					i += n
					continue
				}
				inQuote = false
				continue
			}
			if r == '\r' {
				continue
			}
			if !writeCSVField(&field, &runes, r) {
				return nil, fmt.Errorf("csv_to_json: field too long")
			}
			continue
		}
		if r == qual {
			if saw || field.Len() != 0 {
				return nil, fmt.Errorf("csv_to_json: unexpected quote")
			}
			inQuote = true
			saw = true
			continue
		}
		if r == delim {
			row = append(row, field.String())
			if len(row) > maxCSVColumns {
				return nil, fmt.Errorf("csv_to_json: too many columns")
			}
			field.Reset()
			runes = 0
			saw = false
			continue
		}
		if r == '\n' || r == '\r' {
			if r == '\r' && strings.HasPrefix(text[i:], "\n") {
				i++
			}
			if !saw && len(row) == 0 && field.Len() == 0 {
				continue
			}
			row = append(row, field.String())
			rows = append(rows, row)
			if len(rows) > maxCSVRows {
				return nil, fmt.Errorf("csv_to_json: too many rows")
			}
			row = nil
			field.Reset()
			runes = 0
			saw = false
			continue
		}
		if !writeCSVField(&field, &runes, r) {
			return nil, fmt.Errorf("csv_to_json: field too long")
		}
		saw = true
	}
	if inQuote {
		return nil, fmt.Errorf("csv_to_json: unclosed quote")
	}
	if saw || len(row) > 0 || field.Len() > 0 {
		row = append(row, field.String())
		rows = append(rows, row)
		if len(rows) > maxCSVRows {
			return nil, fmt.Errorf("csv_to_json: too many rows")
		}
	}
	return rows, nil
}

func writeCSVField(field *strings.Builder, runes *int, r rune) bool {
	if *runes >= maxCSVField {
		return false
	}
	field.WriteRune(r)
	*runes++
	return true
}

func csvAsJSON(rows [][]string, header bool) (string, error) {
	if !header {
		if rows == nil {
			rows = [][]string{}
		}
		b, err := json.Marshal(rows)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if len(rows) == 0 {
		return "[]", nil
	}
	names := make([]string, len(rows[0]))
	seen := map[string]bool{}
	for i, name := range rows[0] {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return "", fmt.Errorf("csv_to_json: header columns must be unique and non-empty")
		}
		seen[name] = true
		names[i] = name
	}
	objs := make([]map[string]string, 0, len(rows)-1)
	for i, row := range rows[1:] {
		if len(row) != len(names) {
			return "", fmt.Errorf("csv_to_json: row %d has %d columns", i+2, len(row))
		}
		obj := make(map[string]string, len(names))
		for c, name := range names {
			obj[name] = row[c]
		}
		objs = append(objs, obj)
	}
	b, err := json.Marshal(objs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
