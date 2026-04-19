package views

import (
	"fmt"
	"sort"
	"time"

	"thundercitizen/internal/muni"
	"thundercitizen/internal/munisign"
)

// DataPacksViewModel drives the /data admin page.
type DataPacksViewModel struct {
	Status    muni.StatusSnapshot
	Groups    []PackGroup
	Treemap   TreemapData
	Timeline  TimelineData
	Approved  []munisign.TrustedKey
	Revoked   []munisign.TrustedKey
	TotalRows int64
}

// PackGroup bundles packs of the same unit kind for the detail list.
type PackGroup struct {
	Title    string
	Kind     muni.UnitKind
	EmptyMsg string
	Packs    []PackCard
}

// PackCard is one pack rendered in the detail list below the treemap.
type PackCard struct {
	ID           string
	Kind         muni.UnitKind
	UnitLabel    string
	DatasetCount int
	TotalRows    int64
	AppliedAt    time.Time
	SignerFP     string
	SignerShort  string
	SignerFile   string
	MerkleShort  string
	LastError    string
}

// TreemapData is the D3 payload shape — signer → packs hierarchy.
// Transit-day packs are filtered out at build time (scoped out of the
// visual refresh until we design a month-binning strategy).
type TreemapData struct {
	Signers   []TreemapSigner `json:"signers"`
	TotalRows int64           `json:"total_rows"`
}

type TreemapSigner struct {
	SignerFP    string        `json:"signer_fp"`
	SignerFile  string        `json:"signer_file"`
	SignerShort string        `json:"signer_short"`
	Packs       []TreemapPack `json:"packs"`
	TotalRows   int64         `json:"total_rows"`
	HasError    bool          `json:"has_error"`
}

type TreemapPack struct {
	PackID       string `json:"pack_id"`
	UnitKind     string `json:"unit_kind"`
	UnitLabel    string `json:"unit_label"`
	UnitStart    string `json:"unit_start,omitempty"`
	UnitEnd      string `json:"unit_end,omitempty"`
	DatasetCount int    `json:"dataset_count"`
	TotalRows    int64  `json:"total_rows"`
	AppliedAt    string `json:"applied_at"`
	MerkleShort  string `json:"merkle_short"`
	LastError    string `json:"last_error,omitempty"`
}

// TimelineData drives the horizontal activity strip. Only packs with
// both unit_start and unit_end participate; globals and transit skip.
type TimelineData struct {
	Today time.Time
	Start time.Time // earliest pack start (window widened to include today)
	End   time.Time // latest pack end
	Ticks []TimelineTick
	Rows  []TimelineRow
}

// TimelineTick is a labeled year boundary projected onto the window.
type TimelineTick struct {
	Label string
	Pct   float64
}

type TimelineRow struct {
	PackID    string
	UnitKind  muni.UnitKind
	UnitLabel string
	Start     time.Time
	End       time.Time
	// Percent offsets against the overall Start..End window.
	OffsetPct float64
	WidthPct  float64
	TodayPct  float64
}

// NewDataPacksViewModel formats pack rows into everything the template
// needs: the existing four-group detail list, the treemap hierarchy,
// and the timeline strip.
func NewDataPacksViewModel(status muni.StatusSnapshot, packs []muni.PackRow, approved, revoked []munisign.TrustedKey, signerFile map[string]string) DataPacksViewModel {
	groups := []PackGroup{
		{Title: "Council", Kind: muni.UnitCouncilTerm, EmptyMsg: "No council packs applied yet."},
		{Title: "Budget", Kind: muni.UnitBudgetYear, EmptyMsg: "No budget packs applied yet."},
		{Title: "Transit", Kind: muni.UnitTransitDay, EmptyMsg: "Transit packs accumulate as days roll over."},
		{Title: "Global", Kind: muni.UnitGlobal, EmptyMsg: "No global packs applied yet."},
	}

	var totalRows int64
	for _, p := range packs {
		totalRows += p.TotalRows
		card := PackCard{
			ID:           p.PackID,
			Kind:         p.UnitKind,
			UnitLabel:    formatUnit(p),
			DatasetCount: p.DatasetCount,
			TotalRows:    p.TotalRows,
			AppliedAt:    p.AppliedAt,
			SignerFP:     p.SignerFP,
			SignerShort:  shortFingerprint(p.SignerFP),
			SignerFile:   signerFile[p.SignerFP],
			MerkleShort:  shortHex(p.BundleMerkle, 12),
			LastError:    p.LastError,
		}
		for i := range groups {
			if groups[i].Kind == p.UnitKind {
				groups[i].Packs = append(groups[i].Packs, card)
				break
			}
		}
	}

	return DataPacksViewModel{
		Status:    status,
		Groups:    groups,
		Treemap:   buildTreemap(packs, signerFile),
		Timeline:  buildTimeline(packs),
		Approved:  approved,
		Revoked:   revoked,
		TotalRows: totalRows,
	}
}

// buildTreemap groups packs by signer fingerprint, drops transit-day
// packs (deferred until we design month binning), and buckets packs
// with an empty signer under a synthetic "unsigned" entry so dev
// bundles still render.
func buildTreemap(packs []muni.PackRow, signerFile map[string]string) TreemapData {
	type agg struct {
		signer TreemapSigner
	}
	bySigner := make(map[string]*agg)
	order := []string{}
	var total int64

	for _, p := range packs {
		if p.UnitKind == muni.UnitTransitDay {
			continue
		}
		if p.TotalRows <= 0 {
			// Skip zero-weighted packs — they'd render as slivers.
			// Row counts of zero only happen on fresh schema before
			// any bundle has been applied, so the admin page is
			// uninteresting in that state anyway.
			continue
		}
		key := p.SignerFP
		a, ok := bySigner[key]
		if !ok {
			file := signerFile[key]
			if file == "" {
				if key == "" {
					file = "unsigned"
				} else {
					file = "unknown signer"
				}
			}
			a = &agg{signer: TreemapSigner{
				SignerFP:    key,
				SignerFile:  file,
				SignerShort: shortFingerprint(key),
			}}
			bySigner[key] = a
			order = append(order, key)
		}
		a.signer.Packs = append(a.signer.Packs, TreemapPack{
			PackID:       p.PackID,
			UnitKind:     string(p.UnitKind),
			UnitLabel:    formatUnit(p),
			UnitStart:    formatISO(p.UnitStart),
			UnitEnd:      formatISO(p.UnitEnd),
			DatasetCount: p.DatasetCount,
			TotalRows:    p.TotalRows,
			AppliedAt:    p.AppliedAt.Format("2006-01-02 15:04 MST"),
			MerkleShort:  shortHex(p.BundleMerkle, 12),
			LastError:    p.LastError,
		})
		a.signer.TotalRows += p.TotalRows
		if p.LastError != "" {
			a.signer.HasError = true
		}
		total += p.TotalRows
	}

	signers := make([]TreemapSigner, 0, len(order))
	for _, k := range order {
		s := bySigner[k].signer
		sort.Slice(s.Packs, func(i, j int) bool {
			return s.Packs[i].TotalRows > s.Packs[j].TotalRows
		})
		signers = append(signers, s)
	}
	sort.Slice(signers, func(i, j int) bool {
		return signers[i].TotalRows > signers[j].TotalRows
	})

	return TreemapData{Signers: signers, TotalRows: total}
}

// buildTimeline projects dated packs onto a shared horizontal window.
// Council terms + budget years participate; global packs (no range)
// and transit days (out of scope) are excluded.
func buildTimeline(packs []muni.PackRow) TimelineData {
	var dated []muni.PackRow
	for _, p := range packs {
		if p.UnitKind == muni.UnitTransitDay {
			continue
		}
		if p.UnitStart.IsZero() || p.UnitEnd.IsZero() {
			continue
		}
		dated = append(dated, p)
	}
	if len(dated) == 0 {
		return TimelineData{Today: time.Now()}
	}

	sort.Slice(dated, func(i, j int) bool {
		return dated[i].UnitStart.Before(dated[j].UnitStart)
	})

	start := dated[0].UnitStart
	end := dated[0].UnitEnd
	for _, p := range dated {
		if p.UnitStart.Before(start) {
			start = p.UnitStart
		}
		if p.UnitEnd.After(end) {
			end = p.UnitEnd
		}
	}
	today := time.Now()
	// Widen the window so "today" is visible even if it sits beyond
	// the last pack's end (e.g. mid-term, no future-dated packs yet).
	if today.Before(start) {
		start = today
	}
	if today.After(end) {
		end = today
	}
	span := end.Sub(start).Seconds()
	if span <= 0 {
		span = 1
	}

	rows := make([]TimelineRow, 0, len(dated))
	for _, p := range dated {
		offset := p.UnitStart.Sub(start).Seconds() / span * 100
		width := p.UnitEnd.Sub(p.UnitStart).Seconds() / span * 100
		rows = append(rows, TimelineRow{
			PackID:    p.PackID,
			UnitKind:  p.UnitKind,
			UnitLabel: formatUnit(p),
			Start:     p.UnitStart,
			End:       p.UnitEnd,
			OffsetPct: offset,
			WidthPct:  width,
			TodayPct:  today.Sub(start).Seconds() / span * 100,
		})
	}

	return TimelineData{
		Today: today,
		Start: start,
		End:   end,
		Ticks: buildTimelineTicks(start, end, span),
		Rows:  rows,
	}
}

// buildTimelineTicks emits one labeled tick per calendar year that
// touches the window. The first year's tick is pinned to 0% so the
// leftmost label doesn't sit before the visible area when the window
// starts mid-year (e.g. a term starting in November).
func buildTimelineTicks(start, end time.Time, span float64) []TimelineTick {
	var ticks []TimelineTick
	for year := start.Year(); year <= end.Year(); year++ {
		t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		if t.Before(start) {
			t = start
		}
		if t.After(end) {
			break
		}
		pct := t.Sub(start).Seconds() / span * 100
		ticks = append(ticks, TimelineTick{
			Label: fmt.Sprintf("%d", year),
			Pct:   pct,
		})
	}
	return ticks
}

// StatusBadgeClass maps a muni state to one of the site's --status-*
// tokens so the admin page reuses the phosphor pill system instead of
// inventing new colors.
func StatusBadgeClass(state muni.State) string {
	switch state {
	case muni.StateOK, muni.StateSkipped:
		return "badge-status-ok"
	case muni.StateError:
		return "badge-status-error"
	case muni.StateFetching, muni.StateVerifying, muni.StateApplying:
		return "badge-status-info"
	default:
		return "badge-status-muted"
	}
}

// UnitKindClass maps a unit kind to a CSS class for badge coloring on
// the detail list. The treemap picks its colors in JS via ThemeColors,
// this helper keeps the HTML versions aligned.
func UnitKindClass(k muni.UnitKind) string {
	switch k {
	case muni.UnitBudgetYear:
		return "pack-kind-budget"
	case muni.UnitCouncilTerm:
		return "pack-kind-council"
	case muni.UnitTransitDay:
		return "pack-kind-transit"
	default:
		return "pack-kind-global"
	}
}

func formatUnit(p muni.PackRow) string {
	switch p.UnitKind {
	case muni.UnitBudgetYear:
		if !p.UnitStart.IsZero() {
			return "FY " + p.UnitStart.Format("2006")
		}
	case muni.UnitCouncilTerm:
		if !p.UnitStart.IsZero() && !p.UnitEnd.IsZero() {
			return "Term " + p.UnitStart.Format("2006") + "\u2013" + p.UnitEnd.Format("2006")
		}
	case muni.UnitTransitDay:
		if !p.UnitStart.IsZero() {
			return p.UnitStart.Format("2006-01-02")
		}
	}
	return "—"
}

func formatISO(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func shortFingerprint(fp string) string {
	// SHA256 fingerprints look like "SHA256:abc..." — trim the prefix
	// and cap to 12 chars so the pill stays one-line.
	s := fp
	if len(s) > 7 && s[:7] == "SHA256:" {
		s = s[7:]
	}
	return shortHex(s, 12)
}

func shortHex(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
