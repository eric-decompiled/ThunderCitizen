package views

import (
	"context"
	"fmt"
	"math"

	"thundercitizen/internal/budget"
)

// BuildSankeyFromLedger builds the overview Sankey and service drill-down
// data entirely from the budget ledger database. No editorial metadata
// (descriptions, YoY change strings, percent overrides) is layered on top —
// the ledger is the single source of truth.
func BuildSankeyFromLedger(ctx context.Context, ledger *budget.Ledger, year int, taxLevy, sourceURL, sourceNote string) (SankeyData, map[string]ServiceSankeyJSON, error) {
	sd := SankeyData{
		TaxLevy:     taxLevy,
		SourceURL:   sourceURL,
		SourceNote:  sourceNote,
		Nodes:       []SankeyNode{},
		Links:       []SankeyLink{},
		Details:     map[string]SankeyDetail{},
		LinkDetails: map[string]string{},
	}

	// Query left-side nodes (revenue sources)
	revNodes, err := ledger.SankeyRevenueNodes(ctx, year)
	if err != nil {
		return sd, nil, fmt.Errorf("revenue nodes: %w", err)
	}

	// Query right-side nodes (services)
	svcNodes, err := ledger.SankeyServiceNodes(ctx, year)
	if err != nil {
		return sd, nil, fmt.Errorf("service nodes: %w", err)
	}

	// Query all flows (links)
	flows, err := ledger.SankeyFlows(ctx, year)
	if err != nil {
		return sd, nil, fmt.Errorf("flows: %w", err)
	}

	// Build node list: revenue nodes first (left), then service nodes (right)
	nodeIndex := map[string]int{} // code → index in Nodes slice

	for _, n := range revNodes {
		nodeIndex[n.Code] = len(sd.Nodes)
		sd.Nodes = append(sd.Nodes, SankeyNode{Name: n.Name, Category: "funding"})
		sd.Details[n.Name] = SankeyDetail{
			Total: n.Total / 1_000_000, // DB stores dollars, Sankey uses millions
			Color: n.Color,
		}
	}

	var revenueTotal float64
	for _, n := range revNodes {
		revenueTotal += n.Total / 1_000_000
	}

	var expenseTotal float64
	for _, n := range svcNodes {
		nodeIndex[n.Code] = len(sd.Nodes)
		sd.Nodes = append(sd.Nodes, SankeyNode{Name: n.Name, Category: n.Name})
		val := n.Total / 1_000_000
		expenseTotal += val
		sd.Details[n.Name] = SankeyDetail{
			Total: val,
			Color: n.Color,
		}
	}
	sd.Title = fmt.Sprintf("Operating Budget · $%.1fM", expenseTotal)
	sd.IncomeTotal = fmt.Sprintf("$%.1fM", revenueTotal)
	sd.ExpenseTotal = fmt.Sprintf("$%.1fM", expenseTotal)

	// Build links from flows
	for _, f := range flows {
		srcIdx, srcOk := nodeIndex[f.CreditCode]
		tgtIdx, tgtOk := nodeIndex[f.DebitCode]
		if !srcOk || !tgtOk {
			continue
		}
		val := f.Amount / 1_000_000
		if val < 0.05 {
			continue
		}
		sd.Links = append(sd.Links, SankeyLink{Source: srcIdx, Target: tgtIdx, Value: val})
		if f.Desc != "" {
			key := f.CreditName + "|" + f.DebitName
			sd.LinkDetails[key] = f.Desc
		}
	}

	// Build service drill-down data
	svcDetails := map[string]ServiceSankeyJSON{}
	for _, svc := range svcNodes {
		detail, err := buildServiceDrillDown(ctx, ledger, year, svc, flows)
		if err != nil {
			continue // skip services without drill-down data
		}
		if len(detail.Nodes) > 0 {
			svcDetails[svc.Name] = detail
		}
	}

	return sd, svcDetails, nil
}

// revenueEarmarks maps a revenue code to the sub-account it funds directly.
// These revenues flow 100% to their target rather than being split proportionally.
var revenueEarmarks = map[string]string{
	"revenue.housing_rents": "service.tbdssab_levy.community_housing_homelessness",
}

// subLedgerCreditRollups groups revenue codes under a different display code in
// per-service drill-downs only. TbayTel dividends aren't earmarked to any
// service, so attributing them per-service is misleading — fold into "Other
// Revenue" for the drill-down. The top-level Sankey is unaffected.
var subLedgerCreditRollups = map[string]struct{ Code, Name string }{
	"revenue.tbaytel": {Code: "revenue.other", Name: "Other Revenue"},
}

func rollupCreditForSubLedger(code, name string) (string, string) {
	if r, ok := subLedgerCreditRollups[code]; ok {
		return r.Code, r.Name
	}
	return code, name
}

// buildServiceDrillDown builds the detail Sankey for a single service from tier-2 ledger entries.
func buildServiceDrillDown(ctx context.Context, ledger *budget.Ledger, year int, svc budget.SankeyNode, parentFlows []budget.SankeyFlow) (ServiceSankeyJSON, error) {
	detail := ServiceSankeyJSON{
		Title:   svc.Name,
		Total:   svc.Total / 1_000_000,
		Nodes:   []SankeyNode{},
		Links:   []SankeyLink{},
		Details: map[string]SankeyDetail{},
	}

	// Left nodes: funding sources for this service (from parent tier-1 flows)
	nodeIndex := map[string]int{}
	type revSource struct {
		code string
		idx  int
		val  float64 // millions
	}
	var revSources []revSource
	seenCredit := map[string]int{} // rolled credit code → revSources index
	for _, f := range parentFlows {
		if f.DebitCode != svc.Code {
			continue
		}
		val := f.Amount / 1_000_000
		if val < 0.05 {
			continue
		}
		code, name := rollupCreditForSubLedger(f.CreditCode, f.CreditName)
		if idx, ok := seenCredit[code]; ok {
			revSources[idx].val += val
			d := detail.Details[name]
			d.Total += val
			detail.Details[name] = d
			continue
		}
		nodeIndex[code] = len(detail.Nodes)
		detail.Nodes = append(detail.Nodes, SankeyNode{Name: name, Category: "funding"})
		detail.Details[name] = SankeyDetail{
			Total: val,
			Color: "#9B7D0D",
		}
		revSources = append(revSources, revSource{code: code, idx: nodeIndex[code], val: val})
		seenCredit[code] = len(revSources) - 1
	}

	// Right nodes: sub-accounts
	subNodes, err := ledger.ServiceAccountNodes(ctx, year, svc.Code)
	if err != nil {
		return detail, err
	}
	for _, n := range subNodes {
		nodeIndex[n.Code] = len(detail.Nodes)
		detail.Nodes = append(detail.Nodes, SankeyNode{Name: n.Name, Category: n.Code})
		detail.Details[n.Name] = SankeyDetail{
			Total: n.Total / 1_000_000,
			Color: n.Color,
		}
	}

	// Links: earmarked revenue goes directly, rest is distributed proportionally.
	// 1) Create direct links for earmarked revenue and track consumed amounts.
	earmarked := map[string]float64{} // sub-account code → total earmarked into it
	var poolTotal float64
	for _, rs := range revSources {
		target, ok := revenueEarmarks[rs.code]
		if ok {
			if tgtIdx, found := nodeIndex[target]; found {
				val := math.Round(rs.val*10) / 10
				detail.Links = append(detail.Links, SankeyLink{Source: rs.idx, Target: tgtIdx, Value: val})
				earmarked[target] += rs.val
				continue
			}
		}
		poolTotal += rs.val
	}

	// 2) Distribute pooled (non-earmarked) revenue proportionally across sub-accounts
	//    based on each sub-account's remaining (non-earmarked) amount.
	var remainingTotal float64
	subRemaining := make([]float64, len(subNodes))
	for i, n := range subNodes {
		rem := n.Total/1_000_000 - earmarked[n.Code]
		if rem < 0 {
			rem = 0
		}
		subRemaining[i] = rem
		remainingTotal += rem
	}
	if remainingTotal > 0 && poolTotal > 0 {
		for i, n := range subNodes {
			if subRemaining[i] < 0.05 {
				continue
			}
			tgtIdx := nodeIndex[n.Code]
			share := subRemaining[i] / remainingTotal
			for _, rs := range revSources {
				if _, isEarmarked := revenueEarmarks[rs.code]; isEarmarked {
					continue
				}
				val := math.Round(poolTotal*share*(rs.val/poolTotal)*10) / 10
				if val < 0.05 {
					continue
				}
				detail.Links = append(detail.Links, SankeyLink{Source: rs.idx, Target: tgtIdx, Value: val})
			}
		}
	}

	// Compute income/expense totals
	var incTotal, expTotal float64
	for _, n := range detail.Nodes {
		if d, ok := detail.Details[n.Name]; ok {
			if n.Category == "funding" {
				incTotal += d.Total
			} else {
				expTotal += d.Total
			}
		}
	}
	detail.IncomeTotal = fmt.Sprintf("$%.1fM", incTotal)
	detail.ExpenseTotal = fmt.Sprintf("$%.1fM", expTotal)

	return detail, nil
}
