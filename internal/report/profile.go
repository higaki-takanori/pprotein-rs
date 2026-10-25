package report

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/pprof/profile"
)

// profileData はプロファイルを関数単位に集計したもの。差分計算のため全関数を持つ。
type profileData struct {
	sampleType string
	unit       string
	duration   time.Duration
	total      int64
	flat       map[string]int64
	cum        map[string]int64
}

func loadProfile(path string) (*profileData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open profile: %w", err)
	}
	defer f.Close()

	p, err := profile.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("failed to parse profile: %w", err)
	}
	return summarizeProfile(p), nil
}

func summarizeProfile(p *profile.Profile) *profileData {
	d := &profileData{
		duration: time.Duration(p.DurationNanos),
		flat:     map[string]int64{},
		cum:      map[string]int64{},
	}
	if len(p.SampleType) == 0 {
		return d
	}

	// pprof コマンドと同じく、既定は最後のサンプル種別 (CPU なら cpu/nanoseconds)
	idx := len(p.SampleType) - 1
	if p.DefaultSampleType != "" {
		for i, st := range p.SampleType {
			if st.Type == p.DefaultSampleType {
				idx = i
			}
		}
	}
	d.sampleType = p.SampleType[idx].Type
	d.unit = p.SampleType[idx].Unit

	for _, s := range p.Sample {
		if idx >= len(s.Value) {
			continue
		}
		v := s.Value[idx]
		d.total += v

		seen := map[string]bool{}
		for i, loc := range s.Location {
			names := locationNames(loc)
			for j, name := range names {
				// Location[0].Line[0] がスタックの先頭 (インライン展開された最内関数)
				if i == 0 && j == 0 {
					d.flat[name] += v
				}
				if !seen[name] {
					seen[name] = true
					d.cum[name] += v
				}
			}
		}
	}
	return d
}

func locationNames(loc *profile.Location) []string {
	names := make([]string, 0, len(loc.Line))
	for _, line := range loc.Line {
		if line.Function != nil && line.Function.Name != "" {
			names = append(names, line.Function.Name)
		}
	}
	if len(names) == 0 {
		names = append(names, fmt.Sprintf("0x%x", loc.Address))
	}
	return names
}

func (d *profileData) section(label, id string, top int) ProfileSection {
	rows := make([]ProfileRow, 0, len(d.cum))
	for name, cum := range d.cum {
		rows = append(rows, ProfileRow{
			Function: name,
			Flat:     d.flat[name],
			FlatPct:  percent(float64(d.flat[name]), float64(d.total)),
			Cum:      cum,
			CumPct:   percent(float64(cum), float64(d.total)),
		})
	}

	byFlat := append([]ProfileRow(nil), rows...)
	sort.Slice(byFlat, func(i, j int) bool {
		if byFlat[i].Flat != byFlat[j].Flat {
			return byFlat[i].Flat > byFlat[j].Flat
		}
		return byFlat[i].Function < byFlat[j].Function
	})
	byCum := rows
	sort.Slice(byCum, func(i, j int) bool {
		if byCum[i].Cum != byCum[j].Cum {
			return byCum[i].Cum > byCum[j].Cum
		}
		return byCum[i].Function < byCum[j].Function
	})

	// flat が 0 の関数は flat 順の表に出しても意味がないので落とす
	n := 0
	for n < len(byFlat) && byFlat[n].Flat > 0 {
		n++
	}

	return ProfileSection{
		Label:      label,
		SnapshotID: id,
		SampleType: d.sampleType,
		Unit:       d.unit,
		Duration:   d.duration,
		Total:      d.total,
		ByFlat:     limit(byFlat[:n], top),
		ByCum:      limit(byCum, top),
	}
}

func limit[T any](s []T, n int) []T {
	if n > 0 && len(s) > n {
		return s[:n]
	}
	return s
}
