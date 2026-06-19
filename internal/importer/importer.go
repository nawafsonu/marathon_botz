package importer

import (
	"encoding/csv"
	"errors"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"

	"marathon/internal/race"

	"github.com/xuri/excelize/v2"
)

type Mapping struct {
	BibColumn      string
	NameColumn     string
	PhoneColumn    string
	CategoryColumn string
	NotesColumn    string
}

func ParseUpload(file multipart.File, filename string, mapping Mapping) ([]race.ImportParticipant, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".xlsx":
		return ParseXLSX(file, mapping)
	case ".csv":
		return ParseCSV(file, mapping)
	default:
		return nil, errors.New("upload must be an .xlsx or .csv file")
	}
}

func ParseCSV(reader io.Reader, mapping Mapping) ([]race.ImportParticipant, error) {
	records, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return nil, err
	}
	return rowsToParticipants(records, mapping)
}

func ParseXLSX(reader io.Reader, mapping Mapping) ([]race.ImportParticipant, error) {
	file, err := excelize.OpenReader(reader)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("workbook has no sheets")
	}
	rows, err := file.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	return rowsToParticipants(rows, mapping)
}

func rowsToParticipants(rows [][]string, mapping Mapping) ([]race.ImportParticipant, error) {
	if len(rows) < 2 {
		return nil, errors.New("file must include a header row and at least one runner")
	}
	headers := headerIndex(rows[0])
	nameIndex, ok := columnIndex(headers, mapping.NameColumn)
	if !ok {
		return nil, errors.New("selected name column was not found")
	}
	bibIndex, _ := columnIndex(headers, mapping.BibColumn)
	phoneIndex, _ := columnIndex(headers, mapping.PhoneColumn)
	categoryIndex, _ := columnIndex(headers, mapping.CategoryColumn)
	notesIndex, _ := columnIndex(headers, mapping.NotesColumn)

	participants := make([]race.ImportParticipant, 0, len(rows)-1)
	for _, row := range rows[1:] {
		name := cell(row, nameIndex)
		bib := cell(row, bibIndex)
		phone := cell(row, phoneIndex)
		category := cell(row, categoryIndex)
		notes := cell(row, notesIndex)
		if strings.TrimSpace(name) == "" && strings.TrimSpace(bib) == "" {
			continue
		}
		participants = append(participants, race.ImportParticipant{
			BibNumber:   bib,
			Name:        name,
			PhoneNumber: phone,
			Category:    category,
			Notes:       notes,
		})
	}
	return participants, nil
}

func headerIndex(headers []string) map[string]int {
	index := make(map[string]int, len(headers))
	for i, header := range headers {
		index[normalize(header)] = i
	}
	return index
}

func columnIndex(headers map[string]int, name string) (int, bool) {
	name = normalize(name)
	if name == "" {
		return -1, false
	}
	idx, ok := headers[name]
	return idx, ok
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
