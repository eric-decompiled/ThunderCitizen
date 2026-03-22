-- 000004_stop_visit_entry_exit.up.sql
--
-- Evolve transit.stop_visit from "single observed_at timestamp per visit" to
-- entry + exit + dwell. The GPS proximity pipeline currently records a visit
-- the first moment a bus crosses inside the 50m radius of a stop, which is
-- ~50m of approach distance before the bus actually stops. That first-touch
-- timestamp is systematically early by several seconds and biases the GPS
-- signal on the maintainer audit page at /transit/audit/deltas.
--
-- This migration adds the columns needed for an entry/exit state machine:
--
--   entered_at    — first moment the bus crossed into the 50m radius
--   exited_at     — last moment before it crossed back out (null while active)
--   inside_polls  — how many VehiclePositions polls landed inside the radius
--                   (slice 3: one data point = drive-by, not service)
--
-- After this migration, the tracker (internal/transit/vehicle_tracker.go) will
-- INSERT a row on entry with entered_at + observed_at (both = entry time), then
-- UPDATE the row on exit, setting exited_at, inside_polls, and overwriting
-- observed_at to the midpoint of entered_at and exited_at. All existing readers
-- (recipes/headway.go, chunk.go, repo.go StopAnalytics, audit_queries.go) read
-- observed_at unchanged and automatically inherit the more accurate midpoint.
--
-- Primary key stays (trip_id, stop_id). Cross-day trip_id collisions are a
-- theoretical risk not exercised in practice — GTFS trip_ids are typically
-- service-scoped. The tracker's in-memory activeVisits map is keyed by
-- (trip_id, stop_id, service_date) which prevents runtime collisions even if
-- upstream trip_ids ever repeat.
--
-- No user-facing change: the public route page at /transit/route/:id reads
-- stop_delay.arrival_delay (the GTFS-RT TripUpdate feed), never stop_visit.
-- This migration is entirely in the GPS cross-check pipeline used only on the
-- maintainer audit page.

ALTER TABLE transit.stop_visit
    ADD COLUMN entered_at   timestamptz,
    ADD COLUMN exited_at    timestamptz,
    ADD COLUMN inside_polls smallint;

-- Backfill entered_at from the existing observed_at column so we can enforce
-- NOT NULL below. Existing rows conceptually already store the entry time
-- (the old tracker wrote observed_at = entry time), so the backfill preserves
-- semantics for historical rows.
UPDATE transit.stop_visit
SET entered_at = observed_at
WHERE entered_at IS NULL;

ALTER TABLE transit.stop_visit
    ALTER COLUMN entered_at SET NOT NULL;
