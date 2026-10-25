// Package report は 1 回のベンチ（グループ）分の計測結果を集計し、
// AI に渡しやすい Markdown / JSON にまとめる。
package report

import "time"

type (
	// Report は 1 グループ分の集計結果。format=json ではこの形のまま返す。
	Report struct {
		GroupID  string
		Datetime time.Time
		Top      int

		Git      []GitInfo
		HTTP     []HTTPSection
		SQL      []SQLSection
		Profiles []ProfileSection
		Skipped  []SkippedSnapshot

		Diff *Diff `json:",omitempty"`
	}

	GitInfo struct {
		Labels  []string
		Ref     string
		Hash    string
		Message string
	}

	// SkippedSnapshot は処理中・失敗などで集計に使えなかったスナップショット。
	SkippedSnapshot struct {
		Type    string
		Label   string
		ID      string
		Status  string
		Message string
	}

	HTTPSection struct {
		Label      string
		SnapshotID string
		TotalCount int64
		TotalSum   float64 // 秒
		Rows       []HTTPRow
	}
	HTTPRow struct {
		Method    string
		URI       string
		Count     int64
		Status4xx int64
		Status5xx int64
		Sum       float64
		Avg       float64
		P99       float64
		Max       float64
		Share     float64 // TotalSum に対する割合 (%)
	}

	SQLSection struct {
		Label      string
		SnapshotID string
		TotalCount int64
		TotalSum   float64 // 秒
		Rows       []SQLRow
	}
	SQLRow struct {
		Query           string
		Count           int64
		Sum             float64
		Avg             float64
		Max             float64
		AvgRowsSent     float64
		AvgRowsExamined float64
		Share           float64
	}

	ProfileSection struct {
		Label      string
		SnapshotID string
		SampleType string // 例: "cpu"
		Unit       string // 例: "nanoseconds"
		Duration   time.Duration
		Total      int64
		ByFlat     []ProfileRow
		ByCum      []ProfileRow
	}
	ProfileRow struct {
		Function string
		Flat     int64
		FlatPct  float64
		Cum      int64
		CumPct   float64
	}

	// Diff は比較対象グループ (Base) から今回への変化。
	// 各行は変化量の絶対値が大きい順に Top 件。
	Diff struct {
		BaseGroupID  string
		BaseDatetime time.Time
		HTTP         []DiffSection
		SQL          []DiffSection
		Profiles     []DiffSection
	}
	DiffSection struct {
		Label       string
		Unit        string // "s" または pprof の単位
		BeforeTotal float64
		AfterTotal  float64
		Rows        []DiffRow
	}
	DiffRow struct {
		Key         string // HTTP: "GET /path", SQL: クエリ, pprof: 関数名
		BeforeCount int64
		AfterCount  int64
		Before      float64 // HTTP/SQL: Sum(秒), pprof: Flat
		After       float64
		Delta       float64
	}
)
