// Command auditbudget verifies the budget ledger balances against published totals.
//
// Usage:
//
//	go run ./cmd/auditbudget                 # audit 2026
//	go run ./cmd/auditbudget -year 2023      # audit specific year
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"thundercitizen/internal/budget"
	"thundercitizen/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	year := flag.Int("year", 2026, "fiscal year to audit")
	flag.Parse()

	dsn := config.Secret("DATABASE_URL", "postgres:///thundercitizen")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	ledger := budget.NewLedger(pool)

	fmt.Printf("=== BUDGET LEDGER AUDIT — %d ===\n\n", *year)

	// Grand total
	grandTotal, err := ledger.GrandTotal(ctx, *year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grand total: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("GRAND TOTAL: $%.2fM\n\n", grandTotal/1_000_000)

	// Revenue side (credits)
	revTotals, err := ledger.TotalByRevenue(ctx, *year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "revenue totals: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("REVENUE SOURCES (credit side)")
	revSum := 0.0
	for _, r := range revTotals {
		fmt.Printf("  %-30s $%10.2fM\n", r.Name, r.Total/1_000_000)
		revSum += r.Total
	}
	fmt.Printf("  %-30s $%10.2fM\n\n", "TOTAL", revSum/1_000_000)

	// Expense side (debits by service)
	svcTotals, err := ledger.TotalByService(ctx, *year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "service totals: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("SERVICES (debit side)")
	svcSum := 0.0
	for _, s := range svcTotals {
		fmt.Printf("  %-30s $%10.2fM\n", s.Name, s.Total/1_000_000)
		svcSum += s.Total
	}
	fmt.Printf("  %-30s $%10.2fM\n\n", "TOTAL", svcSum/1_000_000)

	// Balance check
	diff := revSum - svcSum
	if diff > 0.01 || diff < -0.01 {
		fmt.Printf("⚠ IMBALANCE: revenue ($%.2fM) - expenses ($%.2fM) = $%.2fM\n", revSum/1_000_000, svcSum/1_000_000, diff/1_000_000)
	} else {
		fmt.Println("BALANCED: debits == credits")
	}

	// Sub-ledger balance check
	fmt.Println("\n--- SUB-LEDGER BALANCE ---")
	balances, err := ledger.SubLedgerBalances(ctx, *year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sub-ledger balances: %v\n", err)
		os.Exit(1)
	}
	allBalanced := true
	for _, b := range balances {
		status := "OK"
		if b.Difference > 0.01 || b.Difference < -0.01 {
			status = fmt.Sprintf("IMBALANCE $%.2fM", b.Difference/1_000_000)
			allBalanced = false
		}
		fmt.Printf("  %-35s  in=$%8.2fM  out=$%8.2fM  %s\n",
			b.ServiceCode, b.Inflow/1_000_000, b.Allocation/1_000_000, status)
	}
	if allBalanced {
		fmt.Println("  All sub-ledgers balanced")
	}

	// Per-service flows
	fmt.Println("\n--- TIER 1: REVENUE → SERVICES ---")
	flows, err := ledger.FlowsForYear(ctx, *year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flows: %v\n", err)
		os.Exit(1)
	}
	for _, f := range flows {
		fmt.Printf("  %-25s → %-25s $%.1fM\n", f.CreditCode, f.DebitCode, f.Amount/1_000_000)
	}
}
