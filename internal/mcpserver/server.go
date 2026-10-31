// Package mcpserver は pprotein の計測結果を MCP (Model Context Protocol) のツールとして公開する。
// AI エージェントが一覧・レポート・差分の取得、並べ替えや絞り込みでの深掘り、
// 計測の開始を自分で行えるようにする。memo はノイズになるので応答には含めない。
package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kaz/pprotein/internal/collect/group"
	"github.com/kaz/pprotein/internal/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type (
	Deps struct {
		Reports *report.Sources
		// CollectAll は登録済みの全対象の収集を始め、グループIDと対象を返す。
		CollectAll func() (string, []*group.CollectTarget, error)
		// AddMemo はグループに memo を追加する (Web 画面で人が見る用)。
		AddMemo func(groupID, label, text string) error
	}

	server struct {
		deps Deps
	}
)

const instructions = `pprotein collects performance data for ISUCON-style tuning.
One benchmark run is a "group" that holds: httplog (nginx access log aggregated by alp),
slowlog (PostgreSQL slow query log aggregated by slp) and pprof (CPU profile).

Typical workflow:
1. list_groups to see recent runs.
2. get_report (group="latest", compare="prev") for an overview of the latest run and what changed.
3. get_http_stats / get_slow_queries / get_profile to dig into a specific endpoint, query or function.

All times are in seconds. "Share" is the share of the section total, so it shows where time is spent.`

// NewHandler は Streamable HTTP の MCP エンドポイントを返す。
func NewHandler(deps Deps) http.Handler {
	s := NewServer(deps)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

func NewServer(deps Deps) *mcp.Server {
	s := &server{deps: deps}
	srv := mcp.NewServer(&mcp.Implementation{Name: "pprotein", Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_groups",
		Description: "List benchmark runs (groups), newest first, with their snapshots.",
		Annotations: readOnly,
	}, s.listGroups)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_report",
		Description: "Overview of one run: top HTTP endpoints, slow queries and profile functions by total time, " +
			"plus the git commit. Set compare=\"prev\" to also get the change from the previous run.",
		Annotations: readOnly,
	}, s.getReport)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_diff",
		Description: "Only the change between two runs (HTTP, slow queries, profile flat), sorted by the absolute change. " +
			"Negative delta means improvement. Smaller than get_report when you only need to check an optimization.",
		Annotations: readOnly,
	}, s.getDiff)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_http_stats",
		Description: "HTTP endpoint stats of one run (from alp), with sorting and a regex filter on \"METHOD URI\".",
		Annotations: readOnly,
	}, s.getHTTPStats)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_slow_queries",
		Description: "Normalized slow queries of one run (from slp), with sorting and a regex filter on the query text. Query text is not truncated.",
		Annotations: readOnly,
	}, s.getSlowQueries)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_profile",
		Description: "Functions in the CPU profile of one run, by flat (own time) or cum (including callees), " +
			"with a regex filter on the function name (e.g. \"^main\\.\" or your crate name to hide runtime/library code).",
		Annotations: readOnly,
	}, s.getProfile)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_memo",
		Description: "Attach a memo to a run, e.g. the benchmark score and what was changed. It is shown only in the pprotein web UI for humans, not in tool results.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false)},
	}, s.addMemo)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "start_collect",
		Description: "Start collecting all registered targets as a new run. Call this right when the benchmark starts. " +
			"Results are ready after each target's duration; until then the snapshots are pending.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false)},
	}, s.startCollect)

	return srv
}

// ---- 入力 ----

type (
	listGroupsIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"max number of groups to return (default 10)"`
	}
	reportIn struct {
		Group   string `json:"group,omitempty" jsonschema:"group ID from list_groups, or \"latest\" (default)"`
		Compare string `json:"compare,omitempty" jsonschema:"\"prev\" to compare with the previous run, a group ID, or \"none\" (default)"`
		Top     int    `json:"top,omitempty" jsonschema:"rows per table (default 15)"`
	}
	diffIn struct {
		Group string `json:"group,omitempty" jsonschema:"group ID, or \"latest\" (default)"`
		Base  string `json:"base,omitempty" jsonschema:"group ID to compare with, or \"prev\" (default)"`
		Top   int    `json:"top,omitempty" jsonschema:"rows per table (default 15)"`
	}
	httpIn struct {
		Group  string `json:"group,omitempty" jsonschema:"group ID, or \"latest\" (default)"`
		Label  string `json:"label,omitempty" jsonschema:"only this target label (default: all)"`
		Sort   string `json:"sort,omitempty" jsonschema:"one of sum (default), count, avg, p99, max, 5xx"`
		Filter string `json:"filter,omitempty" jsonschema:"regex matched against \"METHOD URI\", e.g. \"^GET /api/isu\""`
		Top    int    `json:"top,omitempty" jsonschema:"max rows (default 30)"`
	}
	sqlIn struct {
		Group  string `json:"group,omitempty" jsonschema:"group ID, or \"latest\" (default)"`
		Label  string `json:"label,omitempty" jsonschema:"only this target label (default: all)"`
		Sort   string `json:"sort,omitempty" jsonschema:"one of sum (default), count, avg, max, rows_examined"`
		Filter string `json:"filter,omitempty" jsonschema:"regex matched against the normalized query, e.g. \"isu_condition\""`
		Top    int    `json:"top,omitempty" jsonschema:"max rows (default 20)"`
	}
	profileIn struct {
		Group  string `json:"group,omitempty" jsonschema:"group ID, or \"latest\" (default)"`
		Label  string `json:"label,omitempty" jsonschema:"only this target label (default: all)"`
		Sort   string `json:"sort,omitempty" jsonschema:"flat (default, own time) or cum (including callees)"`
		Filter string `json:"filter,omitempty" jsonschema:"regex matched against the function name"`
		Top    int    `json:"top,omitempty" jsonschema:"max rows (default 30)"`
	}
	memoIn struct {
		Group string `json:"group,omitempty" jsonschema:"group ID, or \"latest\" (default)"`
		Label string `json:"label,omitempty" jsonschema:"memo label such as \"score\" (default \"memo\")"`
		Text  string `json:"text" jsonschema:"memo text"`
	}
	collectIn struct{}
)

// ---- ツール ----

func (s *server) listGroups(_ context.Context, _ *mcp.CallToolRequest, in listGroupsIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	groups := s.deps.Reports.Groups()
	if len(groups) == 0 {
		return text("No groups yet. Register targets in pprotein and call start_collect when a benchmark starts."), nil, nil
	}

	b := &strings.Builder{}
	fmt.Fprintf(b, "%d groups (showing %d, newest first)\n\n", len(groups), min(limit, len(groups)))
	b.WriteString("| Group | Collected at | Snapshots |\n|---|---|---|\n")
	for _, g := range groups[:min(limit, len(groups))] {
		snaps := make([]string, 0, len(g.Snapshots))
		for _, sn := range g.Snapshots {
			v := sn.Type + ":" + sn.Label
			if sn.Status != "ok" {
				v += " (" + sn.Status + ")"
			}
			snaps = append(snaps, v)
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", g.GroupID, g.Datetime.Format(time.RFC3339), cell(strings.Join(snaps, ", ")))
	}
	return text(b.String()), nil, nil
}

func (s *server) getReport(_ context.Context, _ *mcp.CallToolRequest, in reportIn) (*mcp.CallToolResult, any, error) {
	rep, err := s.deps.Reports.Build(groupOrLatest(in.Group), report.Options{Top: positiveOr(in.Top, report.DefaultTop), Compare: in.Compare})
	if err != nil {
		return nil, nil, err
	}
	buf := &bytes.Buffer{}
	if err := rep.WriteMarkdown(buf); err != nil {
		return nil, nil, err
	}
	return text(buf.String()), nil, nil
}

func (s *server) getDiff(_ context.Context, _ *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, any, error) {
	base := in.Base
	if base == "" {
		base = report.ComparePrev
	}
	if base == report.CompareNone {
		return nil, nil, fmt.Errorf("base must be \"prev\" or a group ID")
	}
	rep, err := s.deps.Reports.Build(groupOrLatest(in.Group), report.Options{Top: positiveOr(in.Top, report.DefaultTop), Compare: base})
	if err != nil {
		return nil, nil, err
	}
	buf := &bytes.Buffer{}
	if err := rep.WriteDiffMarkdown(buf); err != nil {
		return nil, nil, err
	}
	return text(buf.String()), nil, nil
}

func (s *server) getHTTPStats(_ context.Context, _ *mcp.CallToolRequest, in httpIn) (*mcp.CallToolResult, any, error) {
	less, err := httpSorter(in.Sort)
	if err != nil {
		return nil, nil, err
	}
	re, err := compileFilter(in.Filter)
	if err != nil {
		return nil, nil, err
	}
	rep, err := s.full(in.Group)
	if err != nil {
		return nil, nil, err
	}
	secs, err := filterLabel(rep.HTTP, in.Label, func(s report.HTTPSection) string { return s.Label }, "httplog")
	if err != nil {
		return nil, nil, err
	}

	top := positiveOr(in.Top, 30)
	for i := range secs {
		rows := make([]report.HTTPRow, 0, len(secs[i].Rows))
		for _, r := range secs[i].Rows {
			if re == nil || re.MatchString(r.Method+" "+r.URI) {
				rows = append(rows, r)
			}
		}
		sort.SliceStable(rows, func(a, b int) bool { return less(rows[a], rows[b]) })
		secs[i].Rows = head(rows, top)
	}

	buf := &bytes.Buffer{}
	writeQueryHeader(buf, rep.GroupID, in.Sort, "sum", in.Filter, top)
	if err := report.WriteHTTPMarkdown(buf, secs); err != nil {
		return nil, nil, err
	}
	n := 0
	for _, sec := range secs {
		n += len(sec.Rows)
	}
	return text(withNoMatch(buf, n)), nil, nil
}

func (s *server) getSlowQueries(_ context.Context, _ *mcp.CallToolRequest, in sqlIn) (*mcp.CallToolResult, any, error) {
	less, err := sqlSorter(in.Sort)
	if err != nil {
		return nil, nil, err
	}
	re, err := compileFilter(in.Filter)
	if err != nil {
		return nil, nil, err
	}
	rep, err := s.full(in.Group)
	if err != nil {
		return nil, nil, err
	}
	secs, err := filterLabel(rep.SQL, in.Label, func(s report.SQLSection) string { return s.Label }, "slowlog")
	if err != nil {
		return nil, nil, err
	}

	top := positiveOr(in.Top, 20)
	buf := &bytes.Buffer{}
	writeQueryHeader(buf, rep.GroupID, in.Sort, "sum", in.Filter, top)

	// 深掘り用なのでクエリは切り詰めず、表の下に全文を載せる
	for i := range secs {
		rows := make([]report.SQLRow, 0, len(secs[i].Rows))
		for _, r := range secs[i].Rows {
			if re == nil || re.MatchString(r.Query) {
				rows = append(rows, r)
			}
		}
		sort.SliceStable(rows, func(a, b int) bool { return less(rows[a], rows[b]) })
		secs[i].Rows = head(rows, top)

		if err := report.WriteSQLMarkdown(buf, secs[i:i+1]); err != nil {
			return nil, nil, err
		}
		if len(secs[i].Rows) > 0 {
			buf.WriteString("\nFull queries:\n\n")
			for j, r := range secs[i].Rows {
				fmt.Fprintf(buf, "%d. ```sql\n%s\n```\n", j+1, r.Query)
			}
		}
	}
	n := 0
	for _, sec := range secs {
		n += len(sec.Rows)
	}
	return text(withNoMatch(buf, n)), nil, nil
}

func (s *server) getProfile(_ context.Context, _ *mcp.CallToolRequest, in profileIn) (*mcp.CallToolResult, any, error) {
	if in.Sort != "" && in.Sort != "flat" && in.Sort != "cum" {
		return nil, nil, fmt.Errorf("sort must be flat or cum")
	}
	re, err := compileFilter(in.Filter)
	if err != nil {
		return nil, nil, err
	}
	rep, err := s.full(in.Group)
	if err != nil {
		return nil, nil, err
	}
	secs, err := filterLabel(rep.Profiles, in.Label, func(s report.ProfileSection) string { return s.Label }, "pprof")
	if err != nil {
		return nil, nil, err
	}

	top := positiveOr(in.Top, 30)
	keep := func(rows []report.ProfileRow) []report.ProfileRow {
		res := make([]report.ProfileRow, 0, len(rows))
		for _, r := range rows {
			if re == nil || re.MatchString(r.Function) {
				res = append(res, r)
			}
		}
		return head(res, top)
	}
	for i := range secs {
		if in.Sort == "cum" {
			secs[i].ByCum, secs[i].ByFlat = keep(secs[i].ByCum), nil
		} else {
			secs[i].ByFlat, secs[i].ByCum = keep(secs[i].ByFlat), nil
		}
	}

	buf := &bytes.Buffer{}
	writeQueryHeader(buf, rep.GroupID, in.Sort, "flat", in.Filter, top)
	if err := report.WriteProfileMarkdown(buf, secs); err != nil {
		return nil, nil, err
	}
	n := 0
	for _, sec := range secs {
		n += len(sec.ByFlat) + len(sec.ByCum)
	}
	return text(withNoMatch(buf, n)), nil, nil
}

func (s *server) addMemo(_ context.Context, _ *mcp.CallToolRequest, in memoIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, nil, fmt.Errorf("text is required")
	}
	label := in.Label
	if label == "" {
		label = "memo"
	}

	groupID := in.Group
	if groupID == "" || groupID == report.GroupLatest {
		groups := s.deps.Reports.Groups()
		if len(groups) == 0 {
			return nil, nil, fmt.Errorf("no groups yet")
		}
		groupID = groups[0].GroupID
	} else if !s.groupExists(groupID) {
		return nil, nil, fmt.Errorf("%w: %s", report.ErrGroupNotFound, groupID)
	}

	if err := s.deps.AddMemo(groupID, label, in.Text); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Added memo %q to group %s.", label, groupID)), nil, nil
}

func (s *server) startCollect(_ context.Context, _ *mcp.CallToolRequest, _ collectIn) (*mcp.CallToolResult, any, error) {
	groupID, targets, err := s.deps.CollectAll()
	if err != nil {
		return nil, nil, err
	}
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("no targets are registered; add them on the pprotein setting page")
	}

	b := &strings.Builder{}
	fmt.Fprintf(b, "Started group %s.\n\n", groupID)
	longest := 0
	for _, t := range targets {
		fmt.Fprintf(b, "- %s %s: %ds (%s)\n", t.Type, t.Label, t.Duration, t.URL)
		longest = max(longest, t.Duration)
	}
	fmt.Fprintf(b, "\nResults will be ready in about %d seconds. Then call get_report with group=%q.", longest, groupID)
	return text(b.String()), nil, nil
}

// ---- 補助 ----

// full は並べ替え・絞り込みのため、全行入りのレポートを作る。
func (s *server) full(groupID string) (*report.Report, error) {
	return s.deps.Reports.Build(groupOrLatest(groupID), report.Options{Top: -1})
}

func (s *server) groupExists(id string) bool {
	for _, g := range s.deps.Reports.Groups() {
		if g.GroupID == id {
			return true
		}
	}
	return false
}

func filterLabel[T any](secs []T, label string, labelOf func(T) string, typ string) ([]T, error) {
	if len(secs) == 0 {
		return nil, fmt.Errorf("this group has no %s data", typ)
	}
	if label == "" {
		return secs, nil
	}
	var labels []string
	var out []T
	for _, sec := range secs {
		l := labelOf(sec)
		labels = append(labels, l)
		if l == label {
			out = append(out, sec)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("label %q not found in %s; available: %s", label, typ, strings.Join(labels, ", "))
	}
	return out, nil
}

func httpSorter(key string) (func(a, b report.HTTPRow) bool, error) {
	switch key {
	case "", "sum":
		return func(a, b report.HTTPRow) bool { return a.Sum > b.Sum }, nil
	case "count":
		return func(a, b report.HTTPRow) bool { return a.Count > b.Count }, nil
	case "avg":
		return func(a, b report.HTTPRow) bool { return a.Avg > b.Avg }, nil
	case "p99":
		return func(a, b report.HTTPRow) bool { return a.P99 > b.P99 }, nil
	case "max":
		return func(a, b report.HTTPRow) bool { return a.Max > b.Max }, nil
	case "5xx":
		return func(a, b report.HTTPRow) bool { return a.Status5xx > b.Status5xx }, nil
	}
	return nil, fmt.Errorf("sort must be one of sum, count, avg, p99, max, 5xx")
}

func sqlSorter(key string) (func(a, b report.SQLRow) bool, error) {
	switch key {
	case "", "sum":
		return func(a, b report.SQLRow) bool { return a.Sum > b.Sum }, nil
	case "count":
		return func(a, b report.SQLRow) bool { return a.Count > b.Count }, nil
	case "avg":
		return func(a, b report.SQLRow) bool { return a.Avg > b.Avg }, nil
	case "max":
		return func(a, b report.SQLRow) bool { return a.Max > b.Max }, nil
	case "rows_examined":
		return func(a, b report.SQLRow) bool { return a.AvgRowsExamined > b.AvgRowsExamined }, nil
	}
	return nil, fmt.Errorf("sort must be one of sum, count, avg, max, rows_examined")
}

func compileFilter(expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid filter regex: %w", err)
	}
	return re, nil
}

func writeQueryHeader(buf *bytes.Buffer, groupID, sortKey, defaultSort, filter string, top int) {
	if sortKey == "" {
		sortKey = defaultSort
	}
	fmt.Fprintf(buf, "Group %s, sorted by %s, top %d", groupID, sortKey, top)
	if filter != "" {
		fmt.Fprintf(buf, ", filter %q", filter)
	}
	buf.WriteString(". Times are in seconds. Share = share of the section total.\n")
}

func withNoMatch(buf *bytes.Buffer, rows int) string {
	if rows == 0 {
		buf.WriteString("\nNo rows matched. Try a looser filter or another sort key.\n")
	}
	return buf.String()
}

func groupOrLatest(g string) string {
	if g == "" {
		return report.GroupLatest
	}
	return g
}

func positiveOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func boolPtr(v bool) *bool { return &v }
