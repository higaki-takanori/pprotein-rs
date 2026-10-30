package report

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// SQL は bundle_values しても長いことがあるので、Markdown では切り詰める (JSON では全文)
const maxQueryLen = 300

// WriteMarkdown はレポート全体を AI に渡しやすい Markdown で書き出す。
func (r *Report) WriteMarkdown(w io.Writer) error {
	m := &mdWriter{w: w}
	m.header(r)
	m.line("- Tables show top %d rows. Times are in seconds. Share = share of the section total.", r.Top)
	m.http(r.HTTP)
	m.sql(r.SQL)
	m.profiles(r.Profiles)
	if r.Diff != nil {
		m.diff(r.Diff)
	}
	m.skipped(r.Skipped)
	return m.err
}

// WriteDiffMarkdown は見出しと差分だけを書き出す。
func (r *Report) WriteDiffMarkdown(w io.Writer) error {
	m := &mdWriter{w: w}
	m.header(r)
	if r.Diff == nil {
		m.line("")
		m.line("No group to compare with.")
	} else {
		m.diff(r.Diff)
	}
	m.skipped(r.Skipped)
	return m.err
}

// WriteHTTPMarkdown / WriteSQLMarkdown / WriteProfileMarkdown は表だけを書き出す。
// MCP の深掘りツールが並べ替え・絞り込みしたセクションを出すのに使う。
func WriteHTTPMarkdown(w io.Writer, secs []HTTPSection) error {
	m := &mdWriter{w: w}
	m.http(secs)
	return m.err
}

func WriteSQLMarkdown(w io.Writer, secs []SQLSection) error {
	m := &mdWriter{w: w}
	m.sql(secs)
	return m.err
}

// 空の ByFlat / ByCum の表は出さない。
func WriteProfileMarkdown(w io.Writer, secs []ProfileSection) error {
	m := &mdWriter{w: w}
	m.profiles(secs)
	return m.err
}

func (m *mdWriter) header(r *Report) {
	m.line("# pprotein report: %s", r.GroupID)
	m.line("")
	m.line("- Collected at: %s", r.Datetime.Format(time.RFC3339))
	for _, g := range r.Git {
		m.line("- Git (%s): %s@%s %s", strings.Join(g.Labels, ", "), g.Ref, shortHash(g.Hash), subject(g.Message))
	}
	if r.Diff != nil {
		m.line("- Compared with: %s (%s)", r.Diff.BaseGroupID, r.Diff.BaseDatetime.Format(time.RFC3339))
	}
}

func (m *mdWriter) http(secs []HTTPSection) {
	for _, sec := range secs {
		m.line("")
		m.line("## HTTP (alp): %s", sec.Label)
		m.line("")
		m.line("Total: %d requests, %.3fs", sec.TotalCount, sec.TotalSum)
		m.line("")
		m.line("| # | Sum | Share | Count | Avg | P99 | Max | 4xx | 5xx | Method | Uri |")
		m.line("|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|")
		for i, row := range sec.Rows {
			m.line("| %d | %.3f | %.1f%% | %d | %.3f | %.3f | %.3f | %d | %d | %s | %s |",
				i+1, row.Sum, row.Share, row.Count, row.Avg, row.P99, row.Max, row.Status4xx, row.Status5xx,
				cell(row.Method), code(row.URI))
		}
	}
}

func (m *mdWriter) sql(secs []SQLSection) {
	for _, sec := range secs {
		m.line("")
		m.line("## Slow queries (slp): %s", sec.Label)
		m.line("")
		m.line("Total: %d queries, %.3fs", sec.TotalCount, sec.TotalSum)
		m.line("")
		m.line("| # | Sum | Share | Count | Avg | Max | Avg rows sent | Avg rows examined | Query |")
		m.line("|---:|---:|---:|---:|---:|---:|---:|---:|---|")
		for i, row := range sec.Rows {
			m.line("| %d | %.3f | %.1f%% | %d | %.4f | %.4f | %.1f | %.1f | %s |",
				i+1, row.Sum, row.Share, row.Count, row.Avg, row.Max, row.AvgRowsSent, row.AvgRowsExamined,
				code(truncate(row.Query, maxQueryLen)))
		}
	}
}

func (m *mdWriter) profiles(secs []ProfileSection) {
	for _, sec := range secs {
		m.line("")
		m.line("## Profile (pprof): %s", sec.Label)
		m.line("")
		m.line("Sample: %s (%s), duration %s, total %s", sec.SampleType, sec.Unit, sec.Duration, formatValue(sec.Total, sec.Unit))
		if len(sec.ByFlat) > 0 {
			m.line("")
			m.line("### Top by flat (time spent in the function itself)")
			m.line("")
			writeProfileRows(m, sec.ByFlat, sec.Unit)
		}
		if len(sec.ByCum) > 0 {
			m.line("")
			m.line("### Top by cum (including callees)")
			m.line("")
			writeProfileRows(m, sec.ByCum, sec.Unit)
		}
	}
}

func (m *mdWriter) diff(d *Diff) {
	m.line("")
	m.line("## Diff from %s", d.BaseGroupID)
	m.line("")
	m.line("Rows are sorted by the absolute change. Negative Delta means improvement.")
	writeDiff(m, "HTTP", d.HTTP, "Count", "Sum")
	writeDiff(m, "Slow queries", d.SQL, "Count", "Sum")
	writeDiff(m, "Profile flat", d.Profiles, "", "Flat")
	if len(d.HTTP)+len(d.SQL)+len(d.Profiles) == 0 {
		m.line("")
		m.line("No sections with the same label in both groups.")
	}
}

func (m *mdWriter) skipped(skipped []SkippedSnapshot) {
	if len(skipped) == 0 {
		return
	}
	m.line("")
	m.line("## Not included")
	m.line("")
	for _, s := range skipped {
		m.line("- %s %s (%s): %s %s", s.Type, s.Label, s.ID, s.Status, oneLine(s.Message))
	}
}

func writeProfileRows(m *mdWriter, rows []ProfileRow, unit string) {
	m.line("| # | Flat | Flat%% | Cum | Cum%% | Function |")
	m.line("|---:|---:|---:|---:|---:|---|")
	for i, row := range rows {
		m.line("| %d | %s | %.1f%% | %s | %.1f%% | %s |",
			i+1, formatValue(row.Flat, unit), row.FlatPct, formatValue(row.Cum, unit), row.CumPct, code(row.Function))
	}
}

func writeDiff(m *mdWriter, title string, secs []DiffSection, countName, valueName string) {
	for _, sec := range secs {
		fmtV := func(v float64) string {
			if sec.Unit == "s" {
				return fmt.Sprintf("%.3f", v)
			}
			return formatValue(int64(v), sec.Unit)
		}
		fmtD := func(v float64) string {
			s := fmtV(v)
			if v > 0 {
				s = "+" + s
			}
			return s
		}

		m.line("")
		m.line("### %s: %s", title, sec.Label)
		m.line("")
		m.line("Total: %s -> %s (%s)", fmtV(sec.BeforeTotal), fmtV(sec.AfterTotal), fmtD(sec.AfterTotal-sec.BeforeTotal))
		if len(sec.Rows) == 0 {
			continue
		}
		m.line("")
		if countName != "" {
			m.line("| Delta | %s before | %s after | %s before | %s after | Key |", valueName, valueName, countName, countName)
			m.line("|---:|---:|---:|---:|---:|---|")
			for _, row := range sec.Rows {
				m.line("| %s | %s | %s | %d | %d | %s |",
					fmtD(row.Delta), fmtV(row.Before), fmtV(row.After), row.BeforeCount, row.AfterCount, code(truncate(row.Key, maxQueryLen)))
			}
		} else {
			m.line("| Delta | %s before | %s after | Key |", valueName, valueName)
			m.line("|---:|---:|---:|---|")
			for _, row := range sec.Rows {
				m.line("| %s | %s | %s | %s |", fmtD(row.Delta), fmtV(row.Before), fmtV(row.After), code(row.Key))
			}
		}
	}
}

// formatValue は pprof の値を単位に合わせて読みやすくする。
func formatValue(v int64, unit string) string {
	switch unit {
	case "nanoseconds":
		return fmt.Sprintf("%.2fs", float64(v)/1e9)
	case "microseconds":
		return fmt.Sprintf("%.2fs", float64(v)/1e6)
	case "milliseconds":
		return fmt.Sprintf("%.2fs", float64(v)/1e3)
	case "bytes":
		return fmt.Sprintf("%.2fMB", float64(v)/(1<<20))
	default:
		return fmt.Sprintf("%d", v)
	}
}

type mdWriter struct {
	w   io.Writer
	err error
}

func (m *mdWriter) line(format string, args ...any) {
	if m.err != nil {
		return
	}
	_, m.err = fmt.Fprintf(m.w, format+"\n", args...)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}

// code はセルの中身をインラインコードにする。バッククォートを含む SQL でも崩れないよう囲みを伸ばす。
func code(s string) string {
	s = cell(s)
	if s == "" {
		return ""
	}
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || fence != "`" {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// subject はコミットメッセージの 1 行目だけを返す。
func subject(msg string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return strings.TrimSpace(first)
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
