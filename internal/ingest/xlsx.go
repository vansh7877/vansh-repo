package ingest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// ReadXLSX returns the rows of the first worksheet of an .xlsx file.
// It is a small standard-library reader: shared strings, inline strings and
// numbers are supported, which covers bank report exports.
func ReadXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid xlsx file: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	shared, err := readSharedStrings(files["xl/sharedStrings.xml"])
	if err != nil {
		return nil, err
	}
	sheet := firstSheetPath(files)
	f := files[sheet]
	if f == nil {
		return nil, fmt.Errorf("xlsx has no worksheet")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readSheet(rc, shared)
}

func firstSheetPath(files map[string]*zip.File) string {
	type sheet struct {
		RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	}
	var wb struct {
		Sheets []sheet `xml:"sheets>sheet"`
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if decodeXML(files["xl/workbook.xml"], &wb) == nil && len(wb.Sheets) > 0 &&
		decodeXML(files["xl/_rels/workbook.xml.rels"], &rels) == nil {
		for _, r := range rels.Rels {
			if r.ID == wb.Sheets[0].RID {
				if strings.HasPrefix(r.Target, "/") {
					return strings.TrimPrefix(r.Target, "/")
				}
				return path.Join("xl", r.Target)
			}
		}
	}
	return "xl/worksheets/sheet1.xml"
}

func decodeXML(f *zip.File, v any) error {
	if f == nil {
		return fmt.Errorf("missing")
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return xml.NewDecoder(rc).Decode(v)
}

func readSharedStrings(f *zip.File) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var out []string
	var cur strings.Builder
	inSI, inT, inRPh := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("sharedStrings: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = true
			case "rPh": // phonetic hints are not part of the value
				inRPh = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "t":
				inT = false
			case "rPh":
				inRPh = false
			}
		case xml.CharData:
			if inSI && inT && !inRPh {
				cur.Write(t)
			}
		}
	}
}

func readSheet(r io.Reader, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(r)
	var rows [][]string
	var row []string
	var cellType, cellRef string
	var val strings.Builder
	inV, inIS := false, false
	col := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("worksheet: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
				col = 0
			case "c":
				cellType, cellRef = "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						cellType = a.Value
					case "r":
						cellRef = a.Value
					}
				}
				val.Reset()
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				if inIS {
					inV = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				if inIS {
					inV = false
				}
			case "is":
				inIS = false
			case "c":
				idx := col
				if i := colIndex(cellRef); i >= 0 {
					idx = i
				}
				for len(row) <= idx {
					row = append(row, "")
				}
				s := val.String()
				if cellType == "s" {
					if n, err := strconv.Atoi(s); err == nil && n >= 0 && n < len(shared) {
						s = shared[n]
					}
				}
				row[idx] = s
				col = idx + 1
			case "row":
				rows = append(rows, row)
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		}
	}
}

// colIndex converts a cell reference like "AB12" to a zero-based column.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}
