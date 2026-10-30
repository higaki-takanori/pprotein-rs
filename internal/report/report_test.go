package report

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	"github.com/kaz/pprotein/internal/collect"
)

const alpTSV = "Count\t1xx\t2xx\t3xx\t4xx\t5xx\tMethod\tUri\tMin\tMax\tSum\tAvg\tP90\tP95\tP99\tStddev\n" +
	"1\t0\t0\t0\t0\t1\tPOST\t/api/condition/x\t0.010\t0.010\t0.010\t0.010\t0.010\t0.010\t0.010\t0.000\n" +
	"2\t0\t2\t0\t0\t0\tGET\t/api/isu/.+/graph\t0.120\t0.300\t0.420\t0.210\t0.300\t0.300\t0.300\t0.090\n"

const slpTSV = "Count\tQuery\tMin(QueryTime)\tMax(QueryTime)\tSum(QueryTime)\tAvg(QueryTime)\tAvg(RowsSent)\tAvg(RowsExamined)\n" +
	"1\tINSERT INTO `cond` (`a`) VALUES ('S')\t0.1\t0.1\t0.1\t0.1\t1\t10\n" +
	"4\tSELECT * FROM `isu` WHERE `id` = N OR `x` = 'a|b'\t0.1\t0.5\t0.9\t0.225\t10\t1000\n"

func TestParseHTTP(t *testing.T) {
	rows, count, sum, err := parseHTTP(strings.NewReader(alpTSV))
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 || !near(sum, 0.43) {
		t.Fatalf("total = %d, %f", count, sum)
	}
	if rows[0].URI != "/api/isu/.+/graph" || rows[0].Count != 2 || !near(rows[0].P99, 0.3) {
		t.Fatalf("rows not sorted by sum: %+v", rows)
	}
	if !near(rows[0].Share, 0.42/0.43*100) || rows[1].Status5xx != 1 {
		t.Fatalf("unexpected row: %+v", rows)
	}
}

func TestParseSQL(t *testing.T) {
	rows, count, sum, err := parseSQL(strings.NewReader(slpTSV))
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 || !near(sum, 1.0) {
		t.Fatalf("total = %d, %f", count, sum)
	}
	if !strings.HasPrefix(rows[0].Query, "SELECT") || !near(rows[0].Share, 90) || !near(rows[0].AvgRowsExamined, 1000) {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestSummarizeProfile(t *testing.T) {
	p := testProfile(map[string]int64{"main.leaf": 3, "main.other": 1})
	d := summarizeProfile(p)
	if d.sampleType != "cpu" || d.total != 4e9 {
		t.Fatalf("unexpected profile: %+v", d)
	}
	// main.leaf は main.mid にインライン展開されている
	if d.flat["main.leaf"] != 3e9 || d.flat["main.mid"] != 0 || d.cum["main.mid"] != 3e9 || d.cum["main.main"] != 4e9 {
		t.Fatalf("flat=%v cum=%v", d.flat, d.cum)
	}

	sec := d.section("app", "id", 10)
	if sec.ByFlat[0].Function != "main.leaf" || !near(sec.ByFlat[0].FlatPct, 75) || len(sec.ByFlat) != 2 {
		t.Fatalf("byFlat = %+v", sec.ByFlat)
	}
	if sec.ByCum[0].Function != "main.main" || !near(sec.ByCum[0].CumPct, 100) {
		t.Fatalf("byCum = %+v", sec.ByCum)
	}
}

func TestBuildMarkdownWithDiff(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

	old := testProfile(map[string]int64{"main.leaf": 5, "main.other": 1})
	cur := testProfile(map[string]int64{"main.leaf": 1, "main.other": 1})

	src := &Sources{
		HTTPLog: &fakeSource{},
		SlowLog: &fakeSource{},
		PProf:   &fakeSource{},
		bodyPath: func(s *collect.Snapshot) (string, error) {
			return filepath.Join(dir, s.ID), nil
		},
	}
	add := func(s Source, typ, group, label, id string, at time.Time, body string) {
		f := s.(*fakeSource)
		f.entries = append(f.entries, entry(typ, group, label, id, at, collect.StatusOk))
		f.bodies = mapSet(f.bodies, id, body)
	}

	add(src.HTTPLog, "httplog", "g1", "web", "h1", base, strings.Replace(alpTSV, "0.420", "1.420", 1))
	add(src.SlowLog, "slowlog", "g1", "db", "s1", base, slpTSV)
	writeProfile(t, dir, "p1", old)
	add(src.PProf, "pprof", "g1", "app", "p1", base, "")

	at := base.Add(time.Hour)
	add(src.HTTPLog, "httplog", "g2", "web", "h2", at, alpTSV)
	add(src.SlowLog, "slowlog", "g2", "db", "s2", at, slpTSV)
	writeProfile(t, dir, "p2", cur)
	add(src.PProf, "pprof", "g2", "app", "p2", at, "")
	pending := entry("pprof", "g2", "app2", "p3", at, collect.StatusPending)
	src.PProf.(*fakeSource).entries = append(src.PProf.(*fakeSource).entries, pending)

	groups := src.Groups()
	if len(groups) != 2 || groups[0].GroupID != "g2" {
		t.Fatalf("groups = %+v", groups)
	}

	rep, err := src.Build(GroupLatest, Options{Top: 1, Compare: ComparePrev})
	if err != nil {
		t.Fatal(err)
	}
	if rep.GroupID != "g2" || rep.Diff == nil || rep.Diff.BaseGroupID != "g1" {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.HTTP[0].Rows) != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("top or skipped not applied: %+v", rep)
	}
	if d := rep.Diff.HTTP[0].Rows[0]; d.Key != "GET /api/isu/.+/graph" || !near(d.Delta, -1) {
		t.Fatalf("http diff = %+v", d)
	}
	if len(rep.Diff.SQL[0].Rows) != 0 {
		t.Fatalf("sql diff should be empty: %+v", rep.Diff.SQL)
	}
	if d := rep.Diff.Profiles[0].Rows[0]; d.Key != "main.leaf" || d.Delta != -4e9 {
		t.Fatalf("profile diff = %+v", d)
	}

	buf := &bytes.Buffer{}
	if err := rep.WriteMarkdown(buf); err != nil {
		t.Fatal(err)
	}
	md := buf.String()
	for _, want := range []string{
		"# pprotein report: g2",
		"## HTTP (alp): web",
		"| 1 | 0.420 | 97.7% | 2 |",
		"## Slow queries (slp): db",
		"`` SELECT * FROM `isu` WHERE `id` = N OR `x` = 'a\\|b' ``",
		"## Profile (pprof): app",
		"| 1 | 1.00s | 50.0% |",
		"## Diff from g1",
		"| -1.000 | 1.420 | 0.420 | 2 | 2 | `GET /api/isu/.+/graph` |",
		"| -4.00s | 5.00s | 1.00s | `main.leaf` |",
		"- pprof app2 (p3): pending",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown does not contain %q\n%s", want, md)
		}
	}

	if _, err := src.Build("nope", Options{}); err == nil {
		t.Fatal("expected not found")
	}
	if _, err := src.Build("g2", Options{Compare: "nope"}); err == nil {
		t.Fatal("expected not found for compare")
	}
}

// testProfile は main.main -> main.mid (main.leaf をインライン展開) と main.main -> main.other の CPU プロファイル。
// counts は関数ごとの秒数。
func testProfile(counts map[string]int64) *profile.Profile {
	fn := map[string]*profile.Function{}
	for i, name := range []string{"main.main", "main.mid", "main.leaf", "main.other"} {
		fn[name] = &profile.Function{ID: uint64(i + 1), Name: name}
	}
	locMain := &profile.Location{ID: 1, Line: []profile.Line{{Function: fn["main.main"]}}}
	locLeaf := &profile.Location{ID: 2, Line: []profile.Line{{Function: fn["main.leaf"]}, {Function: fn["main.mid"]}}}
	locOther := &profile.Location{ID: 3, Line: []profile.Line{{Function: fn["main.other"]}}}

	p := &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		Location:      []*profile.Location{locMain, locLeaf, locOther},
		Function:      []*profile.Function{fn["main.main"], fn["main.mid"], fn["main.leaf"], fn["main.other"]},
		DurationNanos: int64(30 * time.Second),
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        1e7,
	}
	if n := counts["main.leaf"]; n > 0 {
		p.Sample = append(p.Sample, &profile.Sample{Location: []*profile.Location{locLeaf, locMain}, Value: []int64{n * 100, n * 1e9}})
	}
	if n := counts["main.other"]; n > 0 {
		p.Sample = append(p.Sample, &profile.Sample{Location: []*profile.Location{locOther, locMain}, Value: []int64{n * 100, n * 1e9}})
	}
	return p
}

func writeProfile(t *testing.T, dir, id string, p *profile.Profile) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := p.Write(f); err != nil {
		t.Fatal(err)
	}
}

type fakeSource struct {
	entries []*collect.Entry
	bodies  map[string]string
}

func (f *fakeSource) List() []*collect.Entry { return f.entries }
func (f *fakeSource) Get(id string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.bodies[id])), nil
}

func entry(typ, group, label, id string, at time.Time, status collect.Status) *collect.Entry {
	return &collect.Entry{
		Snapshot: &collect.Snapshot{
			SnapshotMeta:   &collect.SnapshotMeta{Type: typ, ID: id, Datetime: at},
			SnapshotTarget: &collect.SnapshotTarget{GroupId: group, Label: label},
		},
		Status: status,
	}
}

func mapSet(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}

func near(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
