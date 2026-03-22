-- 000003_rename_bucket_to_chunk.up.sql
--
-- Rename transit.route_band_bucket → transit.route_band_chunk along with
-- its index, primary key, and check constraints. Pure rename — no schema
-- shape change, no data movement.
--
-- The rename follows a vocabulary cleanup pass that dropped the
-- "bucket" / "Fridge" metaphor scaffolding in the application code in
-- favor of one neutral word: "chunk". A chunk is one (route_id, date,
-- band) row holding 6 hours of one route's stats. The functional model
-- is unchanged from migration 000002; only the names move.

ALTER TABLE transit.route_band_bucket
    RENAME TO route_band_chunk;

ALTER INDEX transit.idx_transit_route_band_bucket_date
    RENAME TO idx_transit_route_band_chunk_date;

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_bucket_pkey TO route_band_chunk_pkey;

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_bucket_band_check TO route_band_chunk_band_check;

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_bucket_service_kind_check TO route_band_chunk_service_kind_check;
