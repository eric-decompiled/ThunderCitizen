// Package budget provides the double-entry ledger for civic budget transparency.
// Every dollar flowing through the city is recorded as a debit (expense) and
// credit (revenue source), with source citations back to budget documents.
package budget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Account represents a node in the chart of accounts.
type Account struct {
	Code       string
	Name       string
	Type       string // "revenue" or "expense"
	ParentCode string // "" for top-level, e.g. "service.police" for "service.police.personnel"
	Color      string
	SortOrder  int
}

// Entry represents a single ledger entry moving money from a revenue source to an expense.
type Entry struct {
	ID         int64
	FiscalYear int
	DebitCode  string  // where money goes (expense account)
	CreditCode string  // where money comes from (revenue account)
	Amount     float64 // in dollars (not millions)
	BudgetType string  // "operating" or "capital"
	Desc       string
	Notes      string
}

// Hash returns the deterministic content hash used as the budget_ledger
// natural key. Identical Entry contents always produce the same hash;
// any field change yields a different hash.
//
// Source-document URL and page reference are deliberately NOT part of the
// hash — they live with the patch, not with the row. The patch ID stamped
// on each row (`source` column) is the canonical "where did this come from"
// pointer.
func (e Entry) Hash() string {
	// Postgres prints numeric(14,2) values like "100.00" / "1.50" — so we
	// pad to 2 decimal places to match.
	amount := strconv.FormatFloat(e.Amount, 'f', 2, 64)
	parts := []string{
		strconv.Itoa(e.FiscalYear),
		e.DebitCode,
		e.CreditCode,
		amount,
		e.BudgetType,
		e.Desc,
		e.Notes,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// AccountTotal is a summed balance for one account.
type AccountTotal struct {
	Code  string
	Name  string
	Color string
	Total float64
}

// FlowEntry is a source→target flow for Sankey rendering.
type FlowEntry struct {
	CreditCode string
	DebitCode  string
	Amount     float64
}

// SankeyNode is a node with display metadata for the Sankey renderer.
type SankeyNode struct {
	Code      string
	Name      string
	Color     string
	Total     float64
	SortOrder int
}

// SankeyFlow is a link with description for the Sankey renderer.
type SankeyFlow struct {
	CreditCode string
	CreditName string
	DebitCode  string
	DebitName  string
	Amount     float64
	Desc       string
}

// Ledger provides access to the budget ledger tables.
type Ledger struct {
	pool *pgxpool.Pool
}

// NewLedger creates a new Ledger store.
func NewLedger(pool *pgxpool.Pool) *Ledger {
	return &Ledger{pool: pool}
}

// SeedAccounts upserts accounts — inserts new, updates existing with latest name/color/sort.
func (l *Ledger) SeedAccounts(ctx context.Context, accounts []Account) (int, error) {
	inserted := 0
	for _, a := range accounts {
		var parentCode *string
		if a.ParentCode != "" {
			parentCode = &a.ParentCode
		}
		tag, err := l.pool.Exec(ctx, `
			INSERT INTO budget_accounts (code, name, type, parent_code, color, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (code) DO UPDATE SET name = $2, color = $5, sort_order = $6`,
			a.Code, a.Name, a.Type, parentCode, a.Color, a.SortOrder)
		if err != nil {
			return inserted, fmt.Errorf("inserting account %s: %w", a.Code, err)
		}
		if tag.RowsAffected() > 0 {
			inserted++
		}
	}
	return inserted, nil
}

// SeedEntries bulk-inserts ledger entries. ON CONFLICT on source_hash makes
// re-seeding with identical content a no-op; changed content gets a new hash
// and inserts as a new row.
func (l *Ledger) SeedEntries(ctx context.Context, entries []Entry) (int, error) {
	batch := &pgx.Batch{}
	for _, e := range entries {
		batch.Queue(`
			INSERT INTO budget_ledger (fiscal_year, debit_code, credit_code, amount, budget_type, description, notes, source_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (source_hash) DO NOTHING`,
			e.FiscalYear, e.DebitCode, e.CreditCode, e.Amount, e.BudgetType, e.Desc, e.Notes, e.Hash())
	}
	br := l.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range entries {
		if _, err := br.Exec(); err != nil {
			return 0, fmt.Errorf("inserting entry: %w", err)
		}
	}
	return len(entries), nil
}

// DeleteYear hard-deletes all entries for a fiscal year. Used by anyone
// who needs to fully reset a year before re-seeding from a patch.
// Replaces the old soft-delete VoidYear.
func (l *Ledger) DeleteYear(ctx context.Context, year int) error {
	_, err := l.pool.Exec(ctx, `DELETE FROM budget_ledger WHERE fiscal_year = $1`, year)
	return err
}

// TotalByRevenue returns summed credits (revenue sources) for a fiscal year.
func (l *Ledger) TotalByRevenue(ctx context.Context, year int) ([]AccountTotal, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT l.credit_code, a.name, a.color, SUM(l.amount)
		FROM budget_ledger l
		JOIN budget_accounts a ON a.code = l.credit_code
		WHERE l.fiscal_year = $1
		GROUP BY l.credit_code, a.name, a.color
		ORDER BY SUM(l.amount) DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTotals(rows)
}

// TotalByService returns summed debits at the service level (one dot depth) for a fiscal year.
func (l *Ledger) TotalByService(ctx context.Context, year int) ([]AccountTotal, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT
			'service.' || split_part(l.debit_code, '.', 2) AS svc_code,
			a.name, a.color, SUM(l.amount)
		FROM budget_ledger l
		JOIN budget_accounts a ON a.code = 'service.' || split_part(l.debit_code, '.', 2)
		WHERE l.fiscal_year = $1
		GROUP BY svc_code, a.name, a.color
		ORDER BY SUM(l.amount) DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTotals(rows)
}

// FlowsForYear returns all source→service flows for top-level Sankey.
func (l *Ledger) FlowsForYear(ctx context.Context, year int) ([]FlowEntry, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT l.credit_code,
		       'service.' || split_part(l.debit_code, '.', 2) AS svc_code,
		       SUM(l.amount)
		FROM budget_ledger l
		WHERE l.fiscal_year = $1
		GROUP BY l.credit_code, svc_code
		ORDER BY SUM(l.amount) DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var flows []FlowEntry
	for rows.Next() {
		var f FlowEntry
		if err := rows.Scan(&f.CreditCode, &f.DebitCode, &f.Amount); err != nil {
			return nil, err
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}

// DrillDown returns sub-account flows for a specific service.
func (l *Ledger) DrillDown(ctx context.Context, year int, serviceCode string) ([]FlowEntry, error) {
	prefix := serviceCode + ".%"
	rows, err := l.pool.Query(ctx, `
		SELECT l.credit_code, l.debit_code, SUM(l.amount)
		FROM budget_ledger l
		WHERE l.fiscal_year = $1 AND l.debit_code LIKE $2
		GROUP BY l.credit_code, l.debit_code
		ORDER BY SUM(l.amount) DESC`, year, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var flows []FlowEntry
	for rows.Next() {
		var f FlowEntry
		if err := rows.Scan(&f.CreditCode, &f.DebitCode, &f.Amount); err != nil {
			return nil, err
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}

// GrandTotal returns total tier-1 amount (revenue → service) for a fiscal year.
func (l *Ledger) GrandTotal(ctx context.Context, year int) (float64, error) {
	var total float64
	err := l.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM budget_ledger
		WHERE fiscal_year = $1 AND credit_code LIKE 'revenue.%'
		  AND budget_type = 'operating'`, year).Scan(&total)
	return total, err
}

// OperatingSummary holds the computed operating budget totals from the ledger.
type OperatingSummary struct {
	TotalExpenditure float64 // gross operating budget
	PropertyTax      float64 // property_tax + tbaytel revenue
	OtherRevenue     float64 // all non-tax revenue (grants, fees, etc.)
}

// OperatingSummaryForYear computes the operating budget totals from ledger entries.
// Excludes capital entries (budget_type = 'capital') since capital is tracked separately.
func (l *Ledger) OperatingSummaryForYear(ctx context.Context, year int) (*OperatingSummary, error) {
	var s OperatingSummary
	err := l.pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(amount), 0),
			COALESCE(SUM(amount) FILTER (WHERE credit_code IN ('revenue.property_tax', 'revenue.tbaytel')), 0),
			COALESCE(SUM(amount) FILTER (WHERE credit_code NOT IN ('revenue.property_tax', 'revenue.tbaytel')), 0)
		FROM budget_ledger
		WHERE fiscal_year = $1 AND credit_code LIKE 'revenue.%' AND budget_type = 'operating'`,
		year).Scan(&s.TotalExpenditure, &s.PropertyTax, &s.OtherRevenue)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SubLedgerBalance represents the inflow vs allocation balance for a service.
type SubLedgerBalance struct {
	ServiceCode string
	Inflow      float64 // sum of revenue.* → service (tier 1)
	Allocation  float64 // sum of service → service.* (tier 2)
	Difference  float64
}

// SubLedgerBalances checks that each service's inflows match its internal allocations.
func (l *Ledger) SubLedgerBalances(ctx context.Context, year int) ([]SubLedgerBalance, error) {
	rows, err := l.pool.Query(ctx, `
		WITH inflows AS (
			SELECT debit_code AS svc, SUM(amount) AS total
			FROM budget_ledger
			WHERE fiscal_year = $1 AND credit_code LIKE 'revenue.%'
			GROUP BY debit_code
		),
		allocations AS (
			SELECT credit_code AS svc, SUM(amount) AS total
			FROM budget_ledger
			WHERE fiscal_year = $1 AND credit_code LIKE 'service.%' AND debit_code LIKE 'service.%.%'
			GROUP BY credit_code
		)
		SELECT i.svc, i.total, COALESCE(a.total, 0), i.total - COALESCE(a.total, 0)
		FROM inflows i
		LEFT JOIN allocations a ON a.svc = i.svc
		ORDER BY ABS(i.total - COALESCE(a.total, 0)) DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var balances []SubLedgerBalance
	for rows.Next() {
		var b SubLedgerBalance
		if err := rows.Scan(&b.ServiceCode, &b.Inflow, &b.Allocation, &b.Difference); err != nil {
			return nil, err
		}
		balances = append(balances, b)
	}
	return balances, rows.Err()
}

func scanTotals(rows pgx.Rows) ([]AccountTotal, error) {
	var totals []AccountTotal
	for rows.Next() {
		var t AccountTotal
		if err := rows.Scan(&t.Code, &t.Name, &t.Color, &t.Total); err != nil {
			return nil, err
		}
		totals = append(totals, t)
	}
	return totals, rows.Err()
}

// SankeyRevenueNodes returns all revenue account nodes with totals for a year, ordered by sort_order.
func (l *Ledger) SankeyRevenueNodes(ctx context.Context, year int) ([]SankeyNode, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT a.code, a.name, COALESCE(a.color, ''), SUM(l.amount), COALESCE(a.sort_order, 0)
		FROM budget_ledger l
		JOIN budget_accounts a ON a.code = l.credit_code
		WHERE l.fiscal_year = $1 AND l.credit_code LIKE 'revenue.%'
		  AND l.budget_type = 'operating'
		GROUP BY a.code, a.name, a.color, a.sort_order
		ORDER BY a.sort_order`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []SankeyNode
	for rows.Next() {
		var n SankeyNode
		if err := rows.Scan(&n.Code, &n.Name, &n.Color, &n.Total, &n.SortOrder); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// SankeyServiceNodes returns all service-level expense account nodes with totals, ordered by sort_order.
func (l *Ledger) SankeyServiceNodes(ctx context.Context, year int) ([]SankeyNode, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT a.code, a.name, COALESCE(a.color, ''),
		       SUM(l.amount), COALESCE(a.sort_order, 0)
		FROM budget_ledger l
		JOIN budget_accounts a ON a.code = 'service.' || split_part(l.debit_code, '.', 2)
		WHERE l.fiscal_year = $1 AND l.credit_code LIKE 'revenue.%'
		  AND l.budget_type = 'operating'
		GROUP BY a.code, a.name, a.color, a.sort_order
		ORDER BY a.sort_order`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []SankeyNode
	for rows.Next() {
		var n SankeyNode
		if err := rows.Scan(&n.Code, &n.Name, &n.Color, &n.Total, &n.SortOrder); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// SankeyFlows returns all revenue→service flows with descriptions for the overview Sankey.
func (l *Ledger) SankeyFlows(ctx context.Context, year int) ([]SankeyFlow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT l.credit_code, ca.name,
		       'service.' || split_part(l.debit_code, '.', 2) AS svc_code, da.name,
		       SUM(l.amount),
		       COALESCE(string_agg(DISTINCT l.description, '; ') FILTER (WHERE l.description != ''), '')
		FROM budget_ledger l
		JOIN budget_accounts ca ON ca.code = l.credit_code
		JOIN budget_accounts da ON da.code = 'service.' || split_part(l.debit_code, '.', 2)
		WHERE l.fiscal_year = $1 AND l.credit_code LIKE 'revenue.%'
		  AND l.budget_type = 'operating'
		GROUP BY l.credit_code, ca.name, svc_code, da.name
		ORDER BY SUM(l.amount) DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var flows []SankeyFlow
	for rows.Next() {
		var f SankeyFlow
		if err := rows.Scan(&f.CreditCode, &f.CreditName, &f.DebitCode, &f.DebitName, &f.Amount, &f.Desc); err != nil {
			return nil, err
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}

// SankeyDrillDown returns tier-2 flows for a service drill-down with metadata.
func (l *Ledger) SankeyDrillDown(ctx context.Context, year int, serviceCode string) ([]SankeyFlow, error) {
	prefix := serviceCode + ".%"
	rows, err := l.pool.Query(ctx, `
		SELECT l.credit_code, ca.name,
		       l.debit_code, da.name,
		       SUM(l.amount),
		       COALESCE(string_agg(DISTINCT l.description, '; ') FILTER (WHERE l.description != ''), '')
		FROM budget_ledger l
		JOIN budget_accounts ca ON ca.code = l.credit_code
		JOIN budget_accounts da ON da.code = l.debit_code
		WHERE l.fiscal_year = $1 AND l.debit_code LIKE $2
		  AND l.budget_type = 'operating'
		GROUP BY l.credit_code, ca.name, l.debit_code, da.name
		ORDER BY SUM(l.amount) DESC`, year, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var flows []SankeyFlow
	for rows.Next() {
		var f SankeyFlow
		if err := rows.Scan(&f.CreditCode, &f.CreditName, &f.DebitCode, &f.DebitName, &f.Amount, &f.Desc); err != nil {
			return nil, err
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}

// ServiceAccountNodes returns sub-account nodes for a service drill-down.
func (l *Ledger) ServiceAccountNodes(ctx context.Context, year int, serviceCode string) ([]SankeyNode, error) {
	prefix := serviceCode + ".%"
	rows, err := l.pool.Query(ctx, `
		SELECT a.code, a.name, COALESCE(a.color, ''), SUM(l.amount), COALESCE(a.sort_order, 0)
		FROM budget_ledger l
		JOIN budget_accounts a ON a.code = l.debit_code
		WHERE l.fiscal_year = $1 AND l.debit_code LIKE $2
		  AND l.budget_type = 'operating'
		GROUP BY a.code, a.name, a.color, a.sort_order
		ORDER BY SUM(l.amount) DESC`, year, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []SankeyNode
	for rows.Next() {
		var n SankeyNode
		if err := rows.Scan(&n.Code, &n.Name, &n.Color, &n.Total, &n.SortOrder); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// HasEntries checks if the ledger has any entries for a year.
func (l *Ledger) HasEntries(ctx context.Context, year int) (bool, error) {
	var count int
	err := l.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM budget_ledger WHERE fiscal_year = $1`, year).Scan(&count)
	return count > 0, err
}

// VerifyBalance checks that every service sub-ledger balances: inflows from
// revenue sources must equal allocations to sub-accounts. Returns an error
// listing all imbalanced services, or nil if the ledger is clean.
func (l *Ledger) VerifyBalance(ctx context.Context, year int) error {
	balances, err := l.SubLedgerBalances(ctx, year)
	if err != nil {
		return fmt.Errorf("querying balances: %w", err)
	}
	var errs []string
	for _, b := range balances {
		// Allow rounding tolerance of $1 (amounts stored in dollars)
		if b.Difference > 1 || b.Difference < -1 {
			errs = append(errs, fmt.Sprintf("  %s: inflow=$%.2f allocation=$%.2f diff=$%.2f",
				b.ServiceCode, b.Inflow, b.Allocation, b.Difference))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("ledger imbalance for %d:\n%s", year, strings.Join(errs, "\n"))
	}
	return nil
}

// AccountCodeFromName converts a display name to an account code slug.
// "Sworn Officers" → "sworn_officers", "Fire Suppression" → "fire_suppression"
func AccountCodeFromName(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " & ", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	s = strings.ReplaceAll(s, "'", "")
	return s
}
