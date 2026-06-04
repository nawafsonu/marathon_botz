package importer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParseCSVWithMappedColumns(t *testing.T) {
	input := strings.NewReader("Chest No,Runner Name,Mobile\n101,Asha Roy,+91 1\n102,Vikram Sen,+91 2\n")
	rows, err := ParseCSV(input, Mapping{BibColumn: "Chest No", NameColumn: "Runner Name", PhoneColumn: "Mobile"})
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].BibNumber != "101" || rows[0].Name != "Asha Roy" || rows[0].PhoneNumber != "+91 1" {
		t.Fatalf("first row = %+v", rows[0])
	}
}

func TestParseXLSXWithMappedColumns(t *testing.T) {
	file := excelize.NewFile()
	sheet := file.GetSheetName(0)
	_ = file.SetSheetRow(sheet, "A1", &[]string{"Bib", "Name", "Phone"})
	_ = file.SetSheetRow(sheet, "A2", &[]string{"201", "Meera Jain", "+91 3"})
	var buffer bytes.Buffer
	if err := file.Write(&buffer); err != nil {
		t.Fatalf("write workbook: %v", err)
	}

	rows, err := ParseXLSX(bytes.NewReader(buffer.Bytes()), Mapping{BibColumn: "Bib", NameColumn: "Name", PhoneColumn: "Phone"})
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}

	if len(rows) != 1 || rows[0].BibNumber != "201" || rows[0].Name != "Meera Jain" {
		t.Fatalf("rows = %+v", rows)
	}
}
