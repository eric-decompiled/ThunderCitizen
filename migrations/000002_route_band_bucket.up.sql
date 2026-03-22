-- 000002_route_band_bucket.up.sql
--
-- Per-(route_id, date, band) rollup of the raw counts that drive every
-- system metric. Filled by transit.BuildBucketsForDate after a service
-- day closes (or live for "today" via loadOrBuildBuckets); the read path
-- in service.go reads these rows and the bucket package math reaggregates
-- them. Counts and sums are stored, NOT rates, so reaggregation across
-- multiple buckets is exact trip-weighted arithmetic.
--
-- This migration was split out of 000001 because 000001 had already been
-- applied to long-running dev environments by the time the bucket model
-- was added; golang-migrate tracks state by migration number, not file
-- contents, so editing 000001 in place wouldn't have re-run on those DBs.

CREATE TABLE transit.route_band_bucket (
    route_id text NOT NULL,
    date date NOT NULL,
    band text NOT NULL,
    service_kind text NOT NULL,
    trip_count integer NOT NULL,
    on_time_count integer NOT NULL,
    scheduled_count integer NOT NULL,
    cancelled_count integer NOT NULL,
    no_notice_count integer NOT NULL,
    headway_count integer NOT NULL,
    headway_sum_sec double precision NOT NULL,
    headway_sum_sec_sq double precision NOT NULL,
    sched_headway_sec double precision NOT NULL,
    built_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT route_band_bucket_band_check CHECK ((band = ANY (ARRAY['morning'::text, 'midday'::text, 'evening'::text]))),
    CONSTRAINT route_band_bucket_service_kind_check CHECK ((service_kind = ANY (ARRAY['weekday'::text, 'saturday'::text, 'sunday'::text])))
);

ALTER TABLE ONLY transit.route_band_bucket
    ADD CONSTRAINT route_band_bucket_pkey PRIMARY KEY (route_id, date, band);

CREATE INDEX idx_transit_route_band_bucket_date
    ON transit.route_band_bucket USING btree (date);
