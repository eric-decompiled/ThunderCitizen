# Data Visualization Guide

A principles-first reference for Thunder Citizen visualizations. Every chart should earn its place.

## Core Principles

**Data-ink ratio** (Tufte): Maximize the share of ink devoted to data. Remove gridlines, borders, backgrounds, and decoration that don't encode information.

**No chart junk**: No 3D effects, no ornamental patterns, no drop shadows on data elements. If it doesn't help the reader interpret a number, delete it.

**Respect the reader**: Don't require mental arithmetic. Label directly. Sort meaningfully. Use consistent colors across related charts.

## Choosing the Right Chart

### By question asked

| Question | Chart type | Avoid |
|---|---|---|
| How do values compare? | Bar (horizontal if labels are long) | Pie, radar |
| What's the trend over time? | Line | Area (unless stacking matters) |
| What proportion is each part? | Stacked bar, waffle | Pie (angles are hard to read) |
| How is data distributed? | Histogram, density, box + strip | Box alone (hides shape + n) |
| How do two variables relate? | Scatter | Dual-axis line |
| What's the ranking? | Ordered bar, lollipop | Unordered bar |
| What's a single key number? | Big number (stat card) | Chart (overkill) |

### By data shape

| Data | One variable | Two variables | Many groups |
|---|---|---|---|
| Numeric | Histogram, density | Scatter, line | Small multiples |
| Categorical | Bar, lollipop | Grouped bar, heatmap | Treemap, small multiples |
| Numeric + categorical | Bar per group | Grouped bar, dot plot | Faceted charts |
| Time series | Line, area | Multi-line | Small multiples, sparklines |
| Geographic | Choropleth | Bubble map | — |

## Thunder Citizen Data Types

What we actually visualize and recommended forms:

| Data | Example | Recommended |
|---|---|---|
| Budget breakdowns | Police $64.8M, Fire $38.3M, EMS $14.7M | Horizontal bar (sorted by value) |
| Budget over years | 2020-2026 operating budget | Line chart |
| Department proportions | Share of total budget by dept | Stacked bar or waffle |
| Voting records | Councillor yes/no/absent per motion | Heatmap or dot matrix |
| Compensation | Mayor vs councillor pay | Stat cards (current approach works) |
| Transit stats | Routes, stops, accessibility % | Stat cards for totals; bar for comparison |
| Response times | EMS/Fire response distributions | Histogram or box + strip |
| Crime statistics | Year-over-year trends | Line chart; bar for category breakdown |
| Council structure | Ward vs at-large seats | Simple table (not a chart) |

## Common Pitfalls to Avoid

1. **Pie charts** — Human eyes are bad at reading angles. Use bar charts instead.
2. **Truncated y-axis** — Starting at non-zero exaggerates differences. Always start at zero for bar charts. Line charts may truncate with clear labeling.
3. **Dual y-axes** — Allows misleading correlation. Use two separate charts.
4. **Rainbow color scales** — Poor for colorblind users and perceptual uniformity. Use sequential single-hue or diverging two-hue scales.
5. **Unordered bars** — Always sort by value (not alphabetically) unless there's a natural order.
6. **Overplotting** — Too many data points without aggregation. Use opacity, jitter, or binning.
7. **Spaghetti lines** — More than ~5 lines becomes unreadable. Use small multiples or highlight + gray.
8. **Choropleth without normalizing** — Raw counts on maps just show population density. Use per-capita or percentages.
9. **Boxplots hiding sample size** — Always overlay individual points or note n.
10. **Bubble radius vs area** — Size must scale by area, not radius (radius exaggerates differences).

## Color

Use the Thunder Bay palette (`--thunder-*` variables) as the foundation:

- **Sequential data**: Light to dark within one hue (e.g., `--thunder-100` to `--thunder-900`)
- **Categorical data**: Use distinct, accessible hues — max 5-7 before it gets noisy
- **Emphasis**: Use `--accent` to highlight one data point against muted `--thunder-400` for the rest
- **Consistency**: Same color = same category across every chart on the page

## Implementation Notes

- Prefer server-rendered HTML/CSS charts (stat cards, simple bars via `<div>` widths) where possible — no JS required, accessible by default
- For interactive/complex charts: evaluate lightweight options (e.g., Chart.css, or a small JS library)
- Every chart needs: title, axis labels (if applicable), data source citation
- Test at mobile widths — if a chart doesn't work on phone, simplify it

## References

- Tufte, *The Visual Display of Quantitative Information*
- [From Data to Viz](https://www.data-to-viz.com/) — decision tree for chart selection
- [Data Visualization Catalogue](https://datavizcatalogue.com/) — 60+ chart types with use cases
- [EU Data Visualization Guide](https://data.europa.eu/apps/data-visualisation-guide/) — chart junk and data-ink principles
