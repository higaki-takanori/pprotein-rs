package mcpserver

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	"github.com/kaz/pprotein/internal/collect"
	"github.com/kaz/pprotein/internal/collect/group"
	"github.com/kaz/pprotein/internal/event"
	"github.com/kaz/pprotein/internal/report"
	"github.com/kaz/pprotein/internal/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const alpTSV = "Count\t1xx\t2xx\t3xx\t4xx\t5xx\tMethod\tUri\tMin\tMax\tSum\tAvg\tP90\tP95\tP99\tStddev\n" +
	"100\t0\t100\t0\t0\t0\tGET\t/api/trend\t0.1\t0.5\t30.0\t0.300\t0.4\t0.4\t0.5\t0.1\n" +
	"500\t0\t495\t0\t0\t5\tPOST\t/api/condition/x\t0.001\t0.1\t5.0\t0.010\t0.02\t0.03\t0.05\t0.01\n" +
	"10\t0\t10\t0\t0\t0\tGET\t/api/isu/.+/graph\t0.5\t2.0\t%s\t1.0\t1.5\t1.8\t2.0\t0.3\n"

const slpTSV = "Count\tQuery\tMin(QueryTime)\tMax(QueryTime)\tSum(QueryTime)\tAvg(QueryTime)\tAvg(RowsSent)\tAvg(RowsExamined)\n" +
	"200\tSELECT * FROM `isu_condition` WHERE `jia_isu_uuid` = 'S' ORDER BY `timestamp` DESC\t0.01\t0.2\t%s\t0.04\t100\t50000\n" +
	"1000\tSELECT * FROM `isu` WHERE `id` = N\t0.001\t0.01\t2.0\t0.002\t1\t1\n"

// fixture は本物の storage / collector にスナップショットを入れ、MCP クライアントをつなぐ。
type fixture struct {
	t       *testing.T
	session *mcp.ClientSession
	memo    *collect.Collector
	deps    Deps
}

// passThrough はスナップショットの中身をそのまま返す (alp / slp 処理済みの TSV を入れる)。
type passThrough struct{}

func (passThrough) Cacheable() bool { return false }
func (passThrough) Process(s *collect.Snapshot) (io.ReadCloser, error) {
	path, err := s.BodyPath()
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := event.NewHub()
	newCollector := func(typ, ext string) *collect.Collector {
		c, err := collect.New(passThrough{}, &collect.Options{Type: typ, Ext: ext, Store: store, EventHub: hub})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	httplog := newCollector("httplog", "-httplog.log")
	slowlog := newCollector("slowlog", "-slowlog.log")
	pprof := newCollector("pprof", "-pprof.pb.gz")
	memo := newCollector("memo", "-memo.log")

	add := func(c *collect.Collector, groupID, label string, body []byte) {
		if _, err := c.Add(&collect.SnapshotTarget{GroupId: groupID, Label: label}, body); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // グループの時刻がずれるように
	}

	// g1 が改善前、g2 が改善後 (graph と isu_condition が速くなり、main.leaf が減った)
	add(httplog, "g1", "nginx", []byte(strings.Replace(alpTSV, "%s", "40.0", 1)))
	add(slowlog, "g1", "mysql", []byte(strings.Replace(slpTSV, "%s", "8.0", 1)))
	add(pprof, "g1", "app", profileBytes(t, 5))
	add(httplog, "g2", "nginx", []byte(strings.Replace(alpTSV, "%s", "10.0", 1)))
	add(slowlog, "g2", "mysql", []byte(strings.Replace(slpTSV, "%s", "1.0", 1)))
	add(pprof, "g2", "app", profileBytes(t, 1))
	add(memo, "g2", "score", []byte(`{"Text":"score 12345"}`))

	f := &fixture{t: t, memo: memo}
	f.deps = Deps{
		Reports: &report.Sources{HTTPLog: httplog, SlowLog: slowlog, PProf: pprof},
		CollectAll: func() (string, []*group.CollectTarget, error) {
			return "g3", []*group.CollectTarget{
				{Type: "pprof", Label: "app", URL: "http://app/debug/pprof/profile", Duration: 60},
				{Type: "httplog", Label: "nginx", URL: "http://app/debug/log/httplog", Duration: 70},
			}, nil
		},
		AddMemo: func(groupID, label, text string) error {
			_, err := memo.Add(&collect.SnapshotTarget{GroupId: groupID, Label: label}, []byte(`{"Text":"`+text+`"}`))
			return err
		},
	}

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := NewServer(f.deps).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	f.session, err = mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.session.Close() })
	return f
}

// call はツールを呼び、テキストを返す。wantErr ならツールエラーであることを確かめる。
func (f *fixture) call(name string, args map[string]any, wantErr bool) string {
	f.t.Helper()
	res, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError != wantErr {
		f.t.Fatalf("%s: IsError=%v, want %v: %s", name, res.IsError, wantErr, b.String())
	}
	return b.String()
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output does not contain %q\n%s", w, got)
		}
	}
}

func assertNotContains(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(got, u) {
			t.Errorf("output should not contain %q\n%s", u, got)
		}
	}
}

func assertOrder(t *testing.T, got string, wants ...string) {
	t.Helper()
	pos := -1
	for _, w := range wants {
		i := strings.Index(got, w)
		if i < 0 || i < pos {
			t.Fatalf("%q is missing or out of order\n%s", w, got)
		}
		pos = i
	}
}

func TestListTools(t *testing.T) {
	f := newFixture(t)
	res, err := f.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"list_groups", "get_report", "get_diff", "get_http_stats", "get_slow_queries", "get_profile", "add_memo", "start_collect"} {
		if !strings.Contains(got, want) {
			t.Errorf("tool %s is missing: %s", want, got)
		}
	}
}

func TestListGroupsAndReport(t *testing.T) {
	f := newFixture(t)

	out := f.call("list_groups", nil, false)
	assertContains(t, out, "2 groups", "| g2 |", "httplog:nginx")
	assertNotContains(t, out, "score", "memo")
	assertOrder(t, out, "| g2 |", "| g1 |")

	out = f.call("get_report", map[string]any{"compare": "prev", "top": 2}, false)
	assertContains(t, out, "# pprotein report: g2", "## HTTP (alp): nginx", "## Slow queries (slp): mysql",
		"## Profile (pprof): app", "## Diff from g1", "| -30.000 | 40.000 | 10.000 |")
	assertNotContains(t, out, "score 12345", "## Memo", "memo")

	out = f.call("get_diff", map[string]any{"group": "g2"}, false)
	assertContains(t, out, "## Diff from g1", "`GET /api/isu/.+/graph`", "-7.000", "`main.leaf`")
	if strings.Contains(out, "## HTTP (alp)") {
		t.Errorf("get_diff should not include full tables\n%s", out)
	}

	f.call("get_report", map[string]any{"group": "nope"}, true)
	f.call("get_diff", map[string]any{"group": "g1"}, false) // 比較対象がなくてもエラーにしない
}

func TestHTTPStats(t *testing.T) {
	f := newFixture(t)

	out := f.call("get_http_stats", map[string]any{"sort": "count"}, false)
	assertOrder(t, out, "/api/condition/x", "/api/trend", "/api/isu/.+/graph")

	out = f.call("get_http_stats", map[string]any{"filter": "^GET ", "sort": "p99", "top": 1}, false)
	assertContains(t, out, "/api/isu/.+/graph", `filter "^GET "`)
	if strings.Contains(out, "/api/trend") || strings.Contains(out, "/api/condition") {
		t.Errorf("filter or top not applied\n%s", out)
	}

	out = f.call("get_http_stats", map[string]any{"group": "g1", "label": "web"}, true)
	assertContains(t, out, `available: nginx`)
	f.call("get_http_stats", map[string]any{"sort": "bogus"}, true)
	f.call("get_http_stats", map[string]any{"filter": "("}, true)
}

func TestSlowQueries(t *testing.T) {
	f := newFixture(t)

	out := f.call("get_slow_queries", map[string]any{"sort": "rows_examined", "group": "g1"}, false)
	assertOrder(t, out, "isu_condition", "`isu` WHERE")
	assertContains(t, out, "Full queries:", "```sql\nSELECT * FROM `isu_condition`")

	out = f.call("get_slow_queries", map[string]any{"filter": "`isu` WHERE"}, false)
	if strings.Contains(out, "isu_condition") {
		t.Errorf("filter not applied\n%s", out)
	}
}

func TestProfile(t *testing.T) {
	f := newFixture(t)

	out := f.call("get_profile", map[string]any{"group": "g1"}, false)
	assertContains(t, out, "### Top by flat", "`main.leaf`")
	if strings.Contains(out, "### Top by cum") {
		t.Errorf("only flat should be shown\n%s", out)
	}

	out = f.call("get_profile", map[string]any{"sort": "cum", "filter": `^main\.(main|other)$`}, false)
	assertContains(t, out, "### Top by cum", "`main.main`", "`main.other`")
	if strings.Contains(out, "main.leaf") {
		t.Errorf("filter not applied\n%s", out)
	}
	assertContains(t, f.call("get_profile", map[string]any{"filter": "^nothing$"}, false), "No rows matched")
	f.call("get_profile", map[string]any{"sort": "self"}, true)
}

func TestAddMemoAndCollect(t *testing.T) {
	f := newFixture(t)

	out := f.call("add_memo", map[string]any{"label": "change", "text": "add index on isu_condition"}, false)
	assertContains(t, out, `"change"`, "group g2")

	// memo は保存されるが、ツールの応答には出ない
	if n := len(f.memo.List()); n != 2 {
		t.Fatalf("memo count = %d, want 2", n)
	}
	assertNotContains(t, f.call("list_groups", nil, false), "add index", "change")
	assertNotContains(t, f.call("get_report", nil, false), "add index", "change")

	f.call("add_memo", map[string]any{"text": "x", "group": "nope"}, true)
	f.call("add_memo", map[string]any{"text": " "}, true)

	out = f.call("start_collect", nil, false)
	assertContains(t, out, "Started group g3", "httplog nginx: 70s", "about 70 seconds")
}

func TestHTTPHandler(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(NewHandler(f.deps))
	defer srv.Close()

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_groups"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "| g2 |") {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
}

// profileBytes は main.main -> main.leaf (leaf 秒) と main.main -> main.other (1 秒) の CPU プロファイル。
func profileBytes(t *testing.T, leaf int64) []byte {
	t.Helper()
	fns := []*profile.Function{{ID: 1, Name: "main.main"}, {ID: 2, Name: "main.leaf"}, {ID: 3, Name: "main.other"}}
	locs := make([]*profile.Location, len(fns))
	for i, fn := range fns {
		locs[i] = &profile.Location{ID: uint64(i + 1), Line: []profile.Line{{Function: fn}}}
	}
	p := &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        1e7,
		DurationNanos: int64(30 * time.Second),
		Function:      fns,
		Location:      locs,
		Sample: []*profile.Sample{
			{Location: []*profile.Location{locs[1], locs[0]}, Value: []int64{leaf * 100, leaf * 1e9}},
			{Location: []*profile.Location{locs[2], locs[0]}, Value: []int64{100, 1e9}},
		},
	}
	buf := &bytes.Buffer{}
	if err := p.Write(buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
