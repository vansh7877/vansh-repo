package ingest

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// RowError describes a row that could not be parsed.
type RowError struct {
	Row   int    `json:"row"`
	Error string `json:"error"`
}

// Result is the outcome of parsing a file or payload.
type Result struct {
	Txns   []model.Transaction
	Errors []RowError
	Rows   int
}

func (r *Result) addError(row int, err error) {
	if len(r.Errors) < 200 {
		r.Errors = append(r.Errors, RowError{Row: row, Error: err.Error()})
	}
}

// Format is an input file format.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
	FormatJSON Format = "json"
)

// DetectFormat guesses the format from a file name, content type and the first bytes.
func DetectFormat(name, contentType string, head []byte) Format {
	n := strings.ToLower(name)
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), strings.HasSuffix(n, ".xlsx"):
		return FormatXLSX
	case strings.HasSuffix(n, ".json"), strings.Contains(contentType, "json"):
		return FormatJSON
	}
	if t := bytes.TrimLeft(head, " \t\r\n\xef\xbb\xbf"); len(t) > 0 && (t[0] == '[' || t[0] == '{') {
		return FormatJSON
	}
	return FormatCSV
}

// Parse reads transactions in the given format.
func Parse(r io.Reader, f Format, loc *time.Location) (*Result, error) {
	switch f {
	case FormatXLSX:
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		rows, err := ReadXLSX(data)
		if err != nil {
			return nil, err
		}
		return fromRows(rows, loc)
	case FormatJSON:
		return ParseJSON(r, loc)
	default:
		return ParseCSV(r, loc)
	}
}

// ParseCSV reads a delimited file; the delimiter (comma, semicolon, tab, pipe)
// is sniffed from the header line.
func ParseCSV(r io.Reader, loc *time.Location) (*Result, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	if b, _ := br.Peek(3); bytes.Equal(b, []byte("\xef\xbb\xbf")) {
		br.Discard(3)
	}
	head, _ := br.Peek(4096)
	cr := csv.NewReader(br)
	cr.Comma = sniffDelimiter(head)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	m, err := NewMapper(append([]string(nil), header...), loc)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	for line := 2; ; line++ {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			res.addError(line, err)
			continue
		}
		if blank(row) {
			continue
		}
		res.Rows++
		t, err := m.Row(row)
		if err != nil {
			res.addError(line, err)
			continue
		}
		res.Txns = append(res.Txns, t)
	}
	return res, nil
}

func sniffDelimiter(head []byte) rune {
	line := head
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		line = head[:i]
	}
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := bytes.Count(line, []byte(string(d))); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

func blank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// fromRows maps a sheet of rows, finding the header within the first few rows
// (sheets often carry a title line above the table).
func fromRows(rows [][]string, loc *time.Location) (*Result, error) {
	var m *Mapper
	var err error
	start := 0
	for i := 0; i < len(rows) && i < 10; i++ {
		if m, err = NewMapper(rows[i], loc); err == nil {
			start = i + 1
			break
		}
	}
	if m == nil {
		if err == nil {
			err = fmt.Errorf("sheet is empty")
		}
		return nil, err
	}
	res := &Result{}
	for i := start; i < len(rows); i++ {
		if blank(rows[i]) {
			continue
		}
		res.Rows++
		t, err := m.Row(rows[i])
		if err != nil {
			res.addError(i+1, err)
			continue
		}
		res.Txns = append(res.Txns, t)
	}
	return res, nil
}

// ParseJSON accepts a single object, an array of objects, or
// {"transactions": [...]}. Keys are matched loosely like CSV headers.
func ParseJSON(r io.Reader, loc *time.Location) (*Result, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		if inner, ok := v["transactions"].([]any); ok {
			items = inner
		} else {
			items = []any{v}
		}
	default:
		return nil, fmt.Errorf("expected a JSON object or array")
	}
	res := &Result{}
	for i, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			res.addError(i+1, fmt.Errorf("item is not an object"))
			continue
		}
		res.Rows++
		flat := make(map[string]string, len(obj))
		for k, v := range obj {
			flat[k] = cellString(v)
		}
		t, err := FromMap(flat, loc)
		if err != nil {
			res.addError(i+1, err)
			continue
		}
		res.Txns = append(res.Txns, t)
	}
	return res, nil
}
