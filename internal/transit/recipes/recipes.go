// Package recipes is the auditable home for every metric that goes into
// a bottle. Each metric — OTP, cancellation, no-notice cancellation,
// scheduled headway, headway statistics — has its own file with one
// SQL constant and one Go function. The whole definition fits on one
// screen and reads end-to-end without context-switching.
//
// Why a recipe per file:
//
// The metrics in a bottle drive accountability conversations. A
// councillor, a journalist, or a curious resident might ask "where does
// the OTP number come from?" and the answer should be one short SQL
// query, not a 100-line consolidated CTE that computes six things at
// once. Splitting the recipes apart makes each one independently
// auditable: open recipes/otp.go, read 30 lines, understand the entire
// definition. No "scroll up to find the CTE", no "trace which subquery
// feeds which column".
//
// Why it's safe to be wasteful:
//
// We're dealing with civic data volume — about 60 bottles per day on
// the manual fetcher rollup. Five round trips per bottle is 300 queries
// per nightly run, ~300 ms total. The freedom to call separate queries
// (and even re-query the same row across recipes) buys us readability
// for free.
//
// The recipes:
//
//	ServiceKind  → recipes/service_kind.go   weekday | saturday | sunday
//	OTP          → recipes/otp.go            trip count + on-time count
//	Cancel       → recipes/cancel.go         cancellations + no-notice
//	Baseline     → recipes/baseline.go       scheduled trips + headway
//	Headway      → recipes/headway.go        observed headway sums
//
// The orchestrator that assembles a chunk from these recipes lives at
// internal/transit/chunk.go::BuildChunk. It calls each recipe in turn,
// copies the results into a chunk.Chunk struct, and returns it for the
// upsert path to write.
//
// Parameter convention:
//
// Recipe functions take primitive parameters (route_id, date, band
// name, hour bounds) rather than the transit.Band struct. This avoids
// an import cycle (the recipes package can't import transit because
// transit imports recipes) and keeps the SQL parameter binding obvious
// — every recipe takes (ctx, db, routeID, date, …bandPrimitives).
package recipes
