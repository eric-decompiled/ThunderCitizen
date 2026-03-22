package views

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"thundercitizen/internal/budget"
)

// DefaultBudgetYear is the most recent fiscal year the app reports on. The
// budget page defaults to this when no ?year is provided.
const DefaultBudgetYear = 2026

// SankeyData is the JSON structure passed to the D3 sankey renderer.
type SankeyData struct {
	Title        string                  `json:"title,omitempty"`
	TaxLevy      string                  `json:"taxLevy"`
	IncomeTotal  string                  `json:"incomeTotal"`
	ExpenseTotal string                  `json:"expenseTotal"`
	SourceURL    string                  `json:"sourceURL,omitempty"`
	SourceNote   string                  `json:"sourceNote,omitempty"`
	Nodes        []SankeyNode            `json:"nodes"`
	Links        []SankeyLink            `json:"links"`
	Details      map[string]SankeyDetail `json:"details"`
	LinkDetails  map[string]string       `json:"linkDetails,omitempty"`
}

type SankeyNode struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type SankeyLink struct {
	Source int     `json:"source"`
	Target int     `json:"target"`
	Value  float64 `json:"value"`
}

type SankeyDetail struct {
	Total       float64 `json:"total"`
	Percent     int     `json:"percent"`
	Color       string  `json:"color"`
	Description string  `json:"description"`
	Change      string  `json:"change,omitempty"`
}

// BudgetItemView is a pre-formatted budget item for the template. Only the
// display fields the ledger can populate are filled — Summary, Highlights,
// Note, NoteRef stay empty (the template's `if len(...) > 0` / `if x != ""`
// checks elide them naturally).
type BudgetItemView struct {
	Name        string
	AmountLabel string
	PctLabel    string
	BarWidth    string
	Color       string
}

// ServiceSankeyJSON is a detail Sankey for a single service, serializable to JSON.
type ServiceSankeyJSON struct {
	Title        string                  `json:"title"`
	Total        float64                 `json:"total"`
	IncomeTotal  string                  `json:"incomeTotal"`
	ExpenseTotal string                  `json:"expenseTotal"`
	Source       string                  `json:"source"`
	SourceNote   string                  `json:"sourceNote,omitempty"`
	Nodes        []SankeyNode            `json:"nodes"`
	Links        []SankeyLink            `json:"links"`
	Details      map[string]SankeyDetail `json:"details"`
}

// BudgetTopLine holds the revenue/spending summary for the budget page header.
// Capital fields are intentionally absent — the capital ledger isn't loaded
// yet, and the Spending card just shows the operating total without a split.
type BudgetTopLine struct {
	TotalRevenue string
	PropertyTax  string
	TaxPct       string
	OtherSources string
	OtherPct     string
	Operating    string
	HasSpending  bool
}

// BudgetViewModel is the data the budget page renders. When HasData is false
// the template renders an empty state — every other field is the zero value.
type BudgetViewModel struct {
	Year           int
	Years          []int
	HasData        bool
	Items          []BudgetItemView
	ItemsDesc      []BudgetItemView
	TopLine        BudgetTopLine
	Sankey         SankeyData
	ServiceDetails map[string]ServiceSankeyJSON
}

// NewBudgetViewModel builds the view model entirely from the ledger. There is
// no fallback to compiled-in seed data — when the ledger has no entries for the
// requested year, HasData is false and the template renders an empty state.
func NewBudgetViewModel(year int, ctx context.Context, ledger *budget.Ledger) BudgetViewModel {
	vm := BudgetViewModel{
		Year:  year,
		Years: []int{DefaultBudgetYear},
	}

	if ledger == nil || ctx == nil {
		return vm
	}

	hasData, err := ledger.HasEntries(ctx, year)
	if err != nil {
		slog.Warn("budget ledger HasEntries failed", "year", year, "err", err)
		return vm
	}
	if !hasData {
		return vm
	}

	summary, err := ledger.OperatingSummaryForYear(ctx, year)
	if err != nil {
		slog.Warn("budget operating summary failed", "year", year, "err", err)
		return vm
	}

	svcTotals, err := ledger.TotalByService(ctx, year)
	if err != nil {
		slog.Warn("budget service totals failed", "year", year, "err", err)
		return vm
	}

	taxLevyLabel := dollarsToMillionsLabel(summary.PropertyTax)
	totalLabel := dollarsToMillionsLabel(summary.TotalExpenditure)

	sankey, svcDetails, err := BuildSankeyFromLedger(ctx, ledger, year, taxLevyLabel, "", "")
	if err != nil {
		slog.Warn("budget ledger sankey failed", "year", year, "err", err)
		return vm
	}

	// Build per-service items, ascending by amount (small first → big at the
	// bottom of the Sankey list, matching the renderer's row order).
	sort.SliceStable(svcTotals, func(i, j int) bool {
		return svcTotals[i].Total < svcTotals[j].Total
	})

	items := make([]BudgetItemView, 0, len(svcTotals))
	maxAmount := 0.0
	for _, t := range svcTotals {
		if t.Total > maxAmount {
			maxAmount = t.Total
		}
	}
	for _, t := range svcTotals {
		amountM := t.Total / 1_000_000
		pct := 0
		if summary.TotalExpenditure > 0 {
			pct = int(t.Total / summary.TotalExpenditure * 100)
		}
		bar := 4
		if maxAmount > 0 {
			bar = int(t.Total / maxAmount * 100)
			if bar < 4 {
				bar = 4
			}
		}
		items = append(items, BudgetItemView{
			Name:        t.Name,
			AmountLabel: fmt.Sprintf("$%.1fM", amountM),
			PctLabel:    fmt.Sprintf("%d%%", pct),
			BarWidth:    fmt.Sprintf("%d", bar),
			Color:       t.Color,
		})
	}

	itemsDesc := make([]BudgetItemView, len(items))
	copy(itemsDesc, items)
	for i, j := 0, len(itemsDesc)-1; i < j; i, j = i+1, j-1 {
		itemsDesc[i], itemsDesc[j] = itemsDesc[j], itemsDesc[i]
	}

	vm.HasData = true
	vm.Items = items
	vm.ItemsDesc = itemsDesc
	vm.TopLine = BudgetTopLine{
		TotalRevenue: totalLabel,
		PropertyTax:  taxLevyLabel,
		TaxPct:       pctLabel(summary.PropertyTax, summary.TotalExpenditure),
		OtherSources: dollarsToMillionsLabel(summary.OtherRevenue),
		OtherPct:     pctLabel(summary.OtherRevenue, summary.TotalExpenditure),
		Operating:    totalLabel,
		HasSpending:  true,
	}
	vm.Sankey = sankey
	vm.ServiceDetails = svcDetails
	return vm
}

// dollarsToMillionsLabel formats a dollar amount as "$X.XM" suitable for the
// header. Returns "" for non-positive amounts so the template can hide them.
func dollarsToMillionsLabel(dollars float64) string {
	if dollars <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.1fM", dollars/1_000_000)
}

func pctLabel(part, total float64) string {
	if total <= 0 || part <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", part/total*100)
}
