-- Inverse of 000003_rename_bucket_to_chunk.up.sql.

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_chunk_service_kind_check TO route_band_bucket_service_kind_check;

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_chunk_band_check TO route_band_bucket_band_check;

ALTER TABLE transit.route_band_chunk
    RENAME CONSTRAINT route_band_chunk_pkey TO route_band_bucket_pkey;

ALTER INDEX transit.idx_transit_route_band_chunk_date
    RENAME TO idx_transit_route_band_bucket_date;

ALTER TABLE transit.route_band_chunk
    RENAME TO route_band_bucket;
