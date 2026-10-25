package report

import (
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/goccy/go-json"
	"github.com/kaz/pprotein/internal/collect"
)

type (
	// Source は各計測種別の collector。*collect.Collector が満たす。
	Source interface {
		List() []*collect.Entry
		Get(id string) (io.ReadCloser, error)
	}

	Sources struct {
		HTTPLog Source
		SlowLog Source
		PProf   Source
		Memo    Source

		// テスト用。nil なら Snapshot.BodyPath を使う。
		bodyPath func(*collect.Snapshot) (string, error)
	}

	Options struct {
		Top     int    // 各表の行数
		Compare string // "", "none", "prev", またはグループID
	}

	GroupSummary struct {
		GroupID   string
		Datetime  time.Time
		Snapshots []SnapshotSummary
	}
	SnapshotSummary struct {
		Type   string
		Label  string
		ID     string
		Status string
	}
)

const (
	GroupLatest = "latest"
	ComparePrev = "prev"
	CompareNone = "none"
	DefaultTop  = 15
)

var ErrGroupNotFound = fmt.Errorf("group not found")

func (s *Sources) entries() []*collect.Entry {
	var all []*collect.Entry
	for _, src := range []Source{s.HTTPLog, s.SlowLog, s.PProf, s.Memo} {
		if src != nil {
			all = append(all, src.List()...)
		}
	}
	return all
}

// Groups はグループID付きのスナップショットをまとめ、新しい順に返す。
func (s *Sources) Groups() []GroupSummary {
	byID := map[string]*GroupSummary{}
	for _, ent := range s.entries() {
		snap := ent.Snapshot
		if snap == nil || snap.SnapshotMeta == nil || snap.SnapshotTarget == nil || snap.GroupId == "" {
			continue
		}
		g, ok := byID[snap.GroupId]
		if !ok {
			g = &GroupSummary{GroupID: snap.GroupId, Datetime: snap.Datetime}
			byID[snap.GroupId] = g
		}
		if snap.Datetime.Before(g.Datetime) {
			g.Datetime = snap.Datetime
		}
		g.Snapshots = append(g.Snapshots, SnapshotSummary{
			Type:   snap.Type,
			Label:  snap.Label,
			ID:     snap.ID,
			Status: string(ent.Status),
		})
	}

	groups := make([]GroupSummary, 0, len(byID))
	for _, g := range byID {
		sort.Slice(g.Snapshots, func(i, j int) bool {
			a, b := g.Snapshots[i], g.Snapshots[j]
			if a.Type != b.Type {
				return a.Type < b.Type
			}
			return a.Label < b.Label
		})
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if !groups[i].Datetime.Equal(groups[j].Datetime) {
			return groups[i].Datetime.After(groups[j].Datetime)
		}
		return groups[i].GroupID > groups[j].GroupID
	})
	return groups
}

// Build は groupID ("latest" 可) のレポートを作る。
func (s *Sources) Build(groupID string, opts Options) (*Report, error) {
	if opts.Top <= 0 {
		opts.Top = DefaultTop
	}

	groups := s.Groups()
	idx := -1
	for i, g := range groups {
		if g.GroupID == groupID || (groupID == GroupLatest && i == 0) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, groupID)
	}

	var base *GroupSummary
	switch opts.Compare {
	case "", CompareNone:
	case ComparePrev:
		if idx+1 < len(groups) {
			base = &groups[idx+1]
		}
	default:
		for i := range groups {
			if groups[i].GroupID == opts.Compare {
				base = &groups[i]
			}
		}
		if base == nil {
			return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, opts.Compare)
		}
	}

	cur := s.load(groups[idx])
	rep := cur.report
	rep.Top = opts.Top

	if base != nil {
		prev := s.load(*base)
		rep.Diff = buildDiff(prev, cur, opts.Top)
	}

	// 差分は全行から計算するので、表の切り詰めは最後に行う
	for i := range rep.HTTP {
		rep.HTTP[i].Rows = limit(rep.HTTP[i].Rows, opts.Top)
	}
	for i := range rep.SQL {
		rep.SQL[i].Rows = limit(rep.SQL[i].Rows, opts.Top)
	}
	for _, label := range cur.profileLabels {
		p := cur.profiles[label]
		rep.Profiles = append(rep.Profiles, p.data.section(label, p.id, opts.Top))
	}
	return &rep, nil
}

type (
	loaded struct {
		report        Report
		profiles      map[string]*loadedProfile
		profileLabels []string
	}
	loadedProfile struct {
		id   string
		data *profileData
	}
)

func (s *Sources) load(g GroupSummary) *loaded {
	l := &loaded{
		report:   Report{GroupID: g.GroupID, Datetime: g.Datetime},
		profiles: map[string]*loadedProfile{},
	}

	git := map[string]*GitInfo{}
	var gitKeys []string

	for _, ent := range s.groupEntries(g.GroupID) {
		snap := ent.Snapshot
		label := snap.Label

		if repo := snap.Repository; repo != nil && repo.Hash != "" {
			key := repo.Ref + "@" + repo.Hash
			gi, ok := git[key]
			if !ok {
				gi = &GitInfo{Ref: repo.Ref, Hash: repo.Hash, Message: repo.Message}
				git[key] = gi
				gitKeys = append(gitKeys, key)
			}
			if !contains(gi.Labels, label) {
				gi.Labels = append(gi.Labels, label)
			}
		}

		if ent.Status != collect.StatusOk {
			l.skip(ent, string(ent.Status), ent.Message)
			continue
		}
		if err := s.loadEntry(l, ent); err != nil {
			l.skip(ent, "error", err.Error())
		}
	}

	for _, key := range gitKeys {
		l.report.Git = append(l.report.Git, *git[key])
	}
	return l
}

func (s *Sources) loadEntry(l *loaded, ent *collect.Entry) error {
	snap := ent.Snapshot
	label := snap.Label

	switch snap.Type {
	case "httplog":
		r, err := s.HTTPLog.Get(snap.ID)
		if err != nil {
			return err
		}
		defer r.Close()
		rows, count, sum, err := parseHTTP(r)
		if err != nil {
			return err
		}
		l.report.HTTP = append(l.report.HTTP, HTTPSection{
			Label: label, SnapshotID: snap.ID, TotalCount: count, TotalSum: sum, Rows: rows,
		})

	case "slowlog":
		r, err := s.SlowLog.Get(snap.ID)
		if err != nil {
			return err
		}
		defer r.Close()
		rows, count, sum, err := parseSQL(r)
		if err != nil {
			return err
		}
		l.report.SQL = append(l.report.SQL, SQLSection{
			Label: label, SnapshotID: snap.ID, TotalCount: count, TotalSum: sum, Rows: rows,
		})

	case "pprof":
		// pprof collector の Get は Web UI のハンドラを登録し直すので使わず、元ファイルを読む
		bodyPath := (*collect.Snapshot).BodyPath
		if s.bodyPath != nil {
			bodyPath = s.bodyPath
		}
		path, err := bodyPath(snap)
		if err != nil {
			return err
		}
		data, err := loadProfile(path)
		if err != nil {
			return err
		}
		l.profiles[label] = &loadedProfile{id: snap.ID, data: data}
		l.profileLabels = append(l.profileLabels, label)

	case "memo":
		r, err := s.Memo.Get(snap.ID)
		if err != nil {
			return err
		}
		defer r.Close()
		var v struct{ Text string }
		if err := json.NewDecoder(r).Decode(&v); err != nil {
			return fmt.Errorf("failed to decode memo: %w", err)
		}
		l.report.Memos = append(l.report.Memos, Memo{Label: label, Text: v.Text})
	}
	return nil
}

func (l *loaded) skip(ent *collect.Entry, status, msg string) {
	l.report.Skipped = append(l.report.Skipped, SkippedSnapshot{
		Type: ent.Snapshot.Type, Label: ent.Snapshot.Label, ID: ent.Snapshot.ID, Status: status, Message: msg,
	})
}

// groupEntries はグループ内のエントリを種別・ラベル順に返す。
// 同じ種別で同じラベルが複数あるときは、区別できるようラベルにIDを付ける。
func (s *Sources) groupEntries(groupID string) []*collect.Entry {
	var res []*collect.Entry
	for _, ent := range s.entries() {
		snap := ent.Snapshot
		if snap == nil || snap.SnapshotMeta == nil || snap.SnapshotTarget == nil || snap.GroupId != groupID {
			continue
		}
		res = append(res, ent)
	}
	sort.Slice(res, func(i, j int) bool {
		a, b := res[i].Snapshot, res[j].Snapshot
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.ID < b.ID
	})

	count := map[string]int{}
	for _, ent := range res {
		count[ent.Snapshot.Type+"\x00"+ent.Snapshot.Label]++
	}
	for i, ent := range res {
		snap := ent.Snapshot
		if count[snap.Type+"\x00"+snap.Label] > 1 {
			// 元の Snapshot を書き換えないようコピーする
			target := *snap.SnapshotTarget
			target.Label = fmt.Sprintf("%s (%s)", target.Label, snap.ID)
			copied := *snap
			copied.SnapshotTarget = &target
			e := *ent
			e.Snapshot = &copied
			res[i] = &e
		}
	}
	return res
}

type diffValue struct {
	count int64
	value float64
}

func buildDiff(before, after *loaded, top int) *Diff {
	d := &Diff{BaseGroupID: before.report.GroupID, BaseDatetime: before.report.Datetime}

	httpOf := func(l *loaded) map[string]sectionValues {
		m := map[string]sectionValues{}
		for _, sec := range l.report.HTTP {
			v := sectionValues{total: sec.TotalSum, rows: map[string]diffValue{}}
			for _, r := range sec.Rows {
				v.rows[r.Method+" "+r.URI] = diffValue{r.Count, r.Sum}
			}
			m[sec.Label] = v
		}
		return m
	}
	sqlOf := func(l *loaded) map[string]sectionValues {
		m := map[string]sectionValues{}
		for _, sec := range l.report.SQL {
			v := sectionValues{total: sec.TotalSum, rows: map[string]diffValue{}}
			for _, r := range sec.Rows {
				v.rows[r.Query] = diffValue{r.Count, r.Sum}
			}
			m[sec.Label] = v
		}
		return m
	}
	profOf := func(l *loaded) map[string]sectionValues {
		m := map[string]sectionValues{}
		for label, p := range l.profiles {
			v := sectionValues{total: float64(p.data.total), unit: p.data.unit, rows: map[string]diffValue{}}
			for name, flat := range p.data.flat {
				v.rows[name] = diffValue{0, float64(flat)}
			}
			m[label] = v
		}
		return m
	}

	d.HTTP = diffSections(httpOf(before), httpOf(after), "s", top)
	d.SQL = diffSections(sqlOf(before), sqlOf(after), "s", top)
	d.Profiles = diffSections(profOf(before), profOf(after), "", top)
	return d
}

type sectionValues struct {
	total float64
	unit  string
	rows  map[string]diffValue
}

// diffSections は両方に同じラベルがあるセクションだけを比較する。
func diffSections(before, after map[string]sectionValues, unit string, top int) []DiffSection {
	labels := make([]string, 0, len(after))
	for label := range after {
		if _, ok := before[label]; ok {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)

	res := make([]DiffSection, 0, len(labels))
	for _, label := range labels {
		b, a := before[label], after[label]
		u := unit
		if u == "" {
			u = a.unit
		}

		keys := map[string]bool{}
		for k := range b.rows {
			keys[k] = true
		}
		for k := range a.rows {
			keys[k] = true
		}

		rows := make([]DiffRow, 0, len(keys))
		for k := range keys {
			bv, av := b.rows[k], a.rows[k]
			delta := av.value - bv.value
			if delta == 0 && bv.count == av.count {
				continue
			}
			rows = append(rows, DiffRow{
				Key: k, BeforeCount: bv.count, AfterCount: av.count,
				Before: bv.value, After: av.value, Delta: delta,
			})
		}
		sort.Slice(rows, func(i, j int) bool {
			di, dj := math.Abs(rows[i].Delta), math.Abs(rows[j].Delta)
			if di != dj {
				return di > dj
			}
			return rows[i].Key < rows[j].Key
		})

		res = append(res, DiffSection{
			Label: label, Unit: u, BeforeTotal: b.total, AfterTotal: a.total, Rows: limit(rows, top),
		})
	}
	return res
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
