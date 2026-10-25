package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// tsvTable は alp / slp の TSV 出力。列は名前で引くので、列順が変わっても動く。
type tsvTable struct {
	index map[string]int
	rows  [][]string
}

func readTSV(r io.Reader) (*tsvTable, error) {
	cr := csv.NewReader(r)
	cr.Comma = '\t'
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1

	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse tsv: %w", err)
	}

	t := &tsvTable{index: map[string]int{}}
	if len(records) == 0 {
		return t, nil
	}
	for i, h := range records[0] {
		t.index[strings.ToLower(strings.TrimSpace(h))] = i
	}
	t.rows = records[1:]
	return t, nil
}

func (t *tsvTable) str(row []string, col string) string {
	i, ok := t.index[strings.ToLower(col)]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

func (t *tsvTable) float(row []string, col string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(t.str(row, col)), 64)
	return v
}

func (t *tsvTable) int(row []string, col string) int64 {
	return int64(t.float(row, col))
}

// parseHTTP は alp の TSV を Sum の降順で返す。
func parseHTTP(r io.Reader) ([]HTTPRow, int64, float64, error) {
	t, err := readTSV(r)
	if err != nil {
		return nil, 0, 0, err
	}

	var totalCount int64
	var totalSum float64
	rows := make([]HTTPRow, 0, len(t.rows))
	for _, rec := range t.rows {
		row := HTTPRow{
			Method:    t.str(rec, "Method"),
			URI:       t.str(rec, "Uri"),
			Count:     t.int(rec, "Count"),
			Status4xx: t.int(rec, "4xx"),
			Status5xx: t.int(rec, "5xx"),
			Sum:       t.float(rec, "Sum"),
			Avg:       t.float(rec, "Avg"),
			P99:       t.float(rec, "P99"),
			Max:       t.float(rec, "Max"),
		}
		totalCount += row.Count
		totalSum += row.Sum
		rows = append(rows, row)
	}
	for i := range rows {
		rows[i].Share = percent(rows[i].Sum, totalSum)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Sum > rows[j].Sum })
	return rows, totalCount, totalSum, nil
}

// parseSQL は slp の TSV を Sum(QueryTime) の降順で返す。
func parseSQL(r io.Reader) ([]SQLRow, int64, float64, error) {
	t, err := readTSV(r)
	if err != nil {
		return nil, 0, 0, err
	}

	var totalCount int64
	var totalSum float64
	rows := make([]SQLRow, 0, len(t.rows))
	for _, rec := range t.rows {
		row := SQLRow{
			Query:           t.str(rec, "Query"),
			Count:           t.int(rec, "Count"),
			Sum:             t.float(rec, "Sum(QueryTime)"),
			Avg:             t.float(rec, "Avg(QueryTime)"),
			Max:             t.float(rec, "Max(QueryTime)"),
			AvgRowsSent:     t.float(rec, "Avg(RowsSent)"),
			AvgRowsExamined: t.float(rec, "Avg(RowsExamined)"),
		}
		totalCount += row.Count
		totalSum += row.Sum
		rows = append(rows, row)
	}
	for i := range rows {
		rows[i].Share = percent(rows[i].Sum, totalSum)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Sum > rows[j].Sum })
	return rows, totalCount, totalSum, nil
}

func percent(v, total float64) float64 {
	if total == 0 {
		return 0
	}
	return v / total * 100
}
