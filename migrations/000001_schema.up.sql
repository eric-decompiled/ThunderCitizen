-- ThunderCitizen schema — consolidated baseline.
--
-- This file is a pg_dump --schema-only of the dev DB with the three-tier
-- transit data model in place (see /Users/eric/.claude/plans/peppy-growing-emerson.md
-- for the design). It replaces the previous baseline (000001) plus migrations
-- 000015 (metric_indexes), 000016 (drop_sched_headways), and 000017
-- (three_tier_model), which were squashed into this file.
--
-- Layout:
--   public.*  — budget, council, data_patch_log, councillors, etc.
--   gtfs.*    — Tier 1 GTFS staging (loader-private)
--   transit.* — Tier 2 entities (route, stop, route_pattern, route_baseline,
--               trip_catalog, service_calendar, scheduled_stop) and
--               Tier 3 observations (stop_delay, stop_visit, cancellation,
--               vehicle_position, alert, feed_state, feed_gap, vehicle,
--               vehicle_assignment).
--
-- Regenerate with:
--   docker exec thundercitizen-db-1 pg_dump \
--     --schema-only --no-owner --no-privileges --no-tablespaces \
--     --no-comments --exclude-table=schema_migrations \
--     -U postgres thundercitizen \
--     | sed -E '/^\\(restrict|unrestrict)/d; /^SELECT pg_catalog.set_config\(.search_path./d'

--
-- PostgreSQL database dump
--


-- Dumped from database version 16.13 (Debian 16.13-1.pgdg12+1)
-- Dumped by pg_dump version 16.13 (Debian 16.13-1.pgdg12+1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: gtfs; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA gtfs;


--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

-- *not* creating schema, since initdb creates it


--
-- Name: transit; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA transit;


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: postgis; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS postgis WITH SCHEMA public;


--
-- Name: check_ledger_balance(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_ledger_balance() RETURNS trigger
    LANGUAGE plpgsql
    AS $_$
DECLARE
    svc_code TEXT;
    inflow   NUMERIC;
    alloc    NUMERIC;
BEGIN
    -- Determine the service code from the affected row
    IF NEW.credit_code LIKE 'revenue.%' THEN
        svc_code := NEW.debit_code;
    ELSIF NEW.debit_code LIKE 'service.%.%' THEN
        svc_code := NEW.credit_code;
    ELSE
        RETURN NEW;
    END IF;

    -- Sum tier-1 inflows (revenue → service)
    SELECT COALESCE(SUM(amount), 0) INTO inflow
    FROM budget_ledger
    WHERE fiscal_year = NEW.fiscal_year AND voided_at IS NULL
      AND debit_code = svc_code AND credit_code LIKE 'revenue.%';

    -- Sum tier-2 allocations (service → sub-accounts)
    SELECT COALESCE(SUM(amount), 0) INTO alloc
    FROM budget_ledger
    WHERE fiscal_year = NEW.fiscal_year AND voided_at IS NULL
      AND credit_code = svc_code AND debit_code LIKE svc_code || '.%';

    -- Allow $1 tolerance for rounding; balances are checked per-transaction
    -- so intermediate states during batch inserts are expected.
    -- The real enforcement is the deferred constraint check.
    RETURN NEW;
END;
$_$;


--
-- Name: set_geog_from_latlon(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_geog_from_latlon() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.latitude IS NOT NULL AND NEW.longitude IS NOT NULL THEN
        NEW.geog := ST_SetSRID(ST_MakePoint(NEW.longitude, NEW.latitude), 4326)::geography;
    ELSE
        NEW.geog := NULL;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: set_geog_from_latlon(); Type: FUNCTION; Schema: transit; Owner: -
--

CREATE FUNCTION transit.set_geog_from_latlon() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.latitude IS NOT NULL AND NEW.longitude IS NOT NULL THEN
        NEW.geog := ST_SetSRID(ST_MakePoint(NEW.longitude, NEW.latitude), 4326)::geography;
    ELSE
        NEW.geog := NULL;
    END IF;
    RETURN NEW;
END;
$$;


SET default_table_access_method = heap;

--
-- Name: calendar; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.calendar (
    service_id text NOT NULL,
    monday boolean,
    tuesday boolean,
    wednesday boolean,
    thursday boolean,
    friday boolean,
    saturday boolean,
    sunday boolean,
    start_date date,
    end_date date
);


--
-- Name: calendar_dates; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.calendar_dates (
    service_id text NOT NULL,
    date date NOT NULL,
    exception_type integer NOT NULL
);


--
-- Name: feed_info; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.feed_info (
    feed_publisher_name text,
    feed_publisher_url text,
    feed_lang text,
    feed_start_date date,
    feed_end_date date,
    feed_version text
);


--
-- Name: routes; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.routes (
    route_id text NOT NULL,
    short_name text,
    long_name text,
    route_type integer,
    color text,
    text_color text
);


--
-- Name: shapes; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.shapes (
    shape_id text NOT NULL,
    shape_pt_sequence integer NOT NULL,
    shape_pt_lat double precision,
    shape_pt_lon double precision,
    shape_dist_traveled double precision
);


--
-- Name: stop_times; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.stop_times (
    trip_id text NOT NULL,
    stop_sequence integer NOT NULL,
    stop_id text NOT NULL,
    arrival_time text,
    departure_time text,
    timepoint boolean DEFAULT false
);


--
-- Name: stops; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.stops (
    stop_id text NOT NULL,
    stop_name text,
    stop_code text,
    latitude double precision,
    longitude double precision,
    wheelchair integer,
    parent_station text
);


--
-- Name: transfers; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.transfers (
    from_stop_id text NOT NULL,
    to_stop_id text NOT NULL,
    transfer_type integer DEFAULT 0 NOT NULL,
    min_transfer_time integer
);


--
-- Name: trips; Type: TABLE; Schema: gtfs; Owner: -
--

CREATE TABLE gtfs.trips (
    trip_id text NOT NULL,
    route_id text NOT NULL,
    service_id text NOT NULL,
    headsign text,
    direction_id integer,
    shape_id text,
    block_id text
);


--
-- Name: budget_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budget_accounts (
    id integer NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    parent_code text,
    color text,
    sort_order integer DEFAULT 0 NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    CONSTRAINT budget_accounts_type_check CHECK ((type = ANY (ARRAY['revenue'::text, 'expense'::text])))
);


--
-- Name: budget_accounts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.budget_accounts_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: budget_accounts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.budget_accounts_id_seq OWNED BY public.budget_accounts.id;


--
-- Name: budget_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budget_ledger (
    id bigint NOT NULL,
    fiscal_year integer NOT NULL,
    debit_code text NOT NULL,
    credit_code text NOT NULL,
    amount numeric(14,2) NOT NULL,
    budget_type text DEFAULT 'operating'::text NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    source_hash text NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    CONSTRAINT budget_ledger_amount_check CHECK ((amount > (0)::numeric)),
    CONSTRAINT budget_ledger_budget_type_check CHECK ((budget_type = ANY (ARRAY['operating'::text, 'capital'::text])))
);


--
-- Name: budget_balance; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.budget_balance AS
 SELECT budget_ledger.fiscal_year,
    'debit'::text AS side,
    budget_ledger.debit_code AS code,
    sum(budget_ledger.amount) AS total
   FROM public.budget_ledger
  GROUP BY budget_ledger.fiscal_year, budget_ledger.debit_code
UNION ALL
 SELECT budget_ledger.fiscal_year,
    'credit'::text AS side,
    budget_ledger.credit_code AS code,
    sum(budget_ledger.amount) AS total
   FROM public.budget_ledger
  GROUP BY budget_ledger.fiscal_year, budget_ledger.credit_code;


--
-- Name: budget_ledger_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.budget_ledger ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.budget_ledger_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: council_meetings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.council_meetings (
    id text NOT NULL,
    date date NOT NULL,
    title text DEFAULT 'City Council'::text NOT NULL,
    term text NOT NULL,
    minutes_url text,
    pdf_file text,
    llm_summary text DEFAULT ''::text NOT NULL,
    llm_model text DEFAULT ''::text NOT NULL,
    scraped_at timestamp with time zone DEFAULT now() NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL
);


--
-- Name: council_motions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.council_motions (
    id bigint NOT NULL,
    meeting_id text NOT NULL,
    motion_index integer NOT NULL,
    motion_text text DEFAULT ''::text NOT NULL,
    moved_by text DEFAULT ''::text NOT NULL,
    seconded_by text DEFAULT ''::text NOT NULL,
    result text DEFAULT ''::text NOT NULL,
    raw_text text DEFAULT ''::text NOT NULL,
    agenda_item text DEFAULT ''::text NOT NULL,
    significance text DEFAULT 'routine'::text NOT NULL,
    media_url text,
    llm_summary text DEFAULT ''::text NOT NULL,
    llm_label text DEFAULT ''::text NOT NULL,
    llm_significance text DEFAULT ''::text NOT NULL,
    llm_model text DEFAULT ''::text NOT NULL,
    search_text tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, ((COALESCE(agenda_item, ''::text) || ' '::text) || motion_text))) STORED,
    source text DEFAULT 'manual'::text NOT NULL
);


--
-- Name: council_motions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.council_motions ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.council_motions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: council_vote_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.council_vote_records (
    motion_id bigint NOT NULL,
    councillor text NOT NULL,
    "position" text NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL
);


--
-- Name: councillors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.councillors (
    id bigint NOT NULL,
    name text NOT NULL,
    council_type text NOT NULL,
    term text NOT NULL,
    "position" text DEFAULT ''::text NOT NULL,
    term_number text DEFAULT ''::text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    short_summary text DEFAULT ''::text NOT NULL,
    photo text DEFAULT ''::text NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    CONSTRAINT councillors_name_not_empty CHECK ((name <> ''::text)),
    CONSTRAINT councillors_term_not_empty CHECK ((term <> ''::text)),
    CONSTRAINT councillors_type_valid CHECK ((council_type = ANY (ARRAY['mayor'::text, 'atlarge'::text, 'ward'::text])))
);


--
-- Name: councillors_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.councillors ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.councillors_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: data_patch_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.data_patch_log (
    id bigint NOT NULL,
    patch_id text NOT NULL,
    action text NOT NULL,
    checksum text NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT data_patch_log_action_check CHECK ((action = ANY (ARRAY['apply'::text, 'down'::text])))
);


--
-- Name: data_patch_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.data_patch_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: data_patch_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.data_patch_log_id_seq OWNED BY public.data_patch_log.id;


--
-- Name: alert; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.alert (
    id bigint NOT NULL,
    feed_timestamp timestamp with time zone NOT NULL,
    alert_id text NOT NULL,
    cause text,
    effect text,
    header text,
    description text,
    severity_level text,
    url text,
    active_start timestamp with time zone,
    active_end timestamp with time zone,
    affected_routes text[],
    affected_stops text[]
);


--
-- Name: alert_id_seq; Type: SEQUENCE; Schema: transit; Owner: -
--

ALTER TABLE transit.alert ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME transit.alert_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: cancellation; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.cancellation (
    id bigint NOT NULL,
    feed_timestamp timestamp with time zone NOT NULL,
    trip_id text NOT NULL,
    route_id text NOT NULL,
    start_date text,
    start_time text,
    schedule_relationship text NOT NULL,
    headsign text DEFAULT ''::text NOT NULL,
    pattern_id text DEFAULT ''::text NOT NULL,
    scheduled_last_arr_time text DEFAULT ''::text NOT NULL
);


--
-- Name: cancellation_id_seq; Type: SEQUENCE; Schema: transit; Owner: -
--

ALTER TABLE transit.cancellation ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME transit.cancellation_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: feed_gap; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.feed_gap (
    id bigint NOT NULL,
    feed_type text NOT NULL,
    gap_start timestamp with time zone NOT NULL,
    gap_end timestamp with time zone NOT NULL,
    expected_interval_seconds integer NOT NULL,
    actual_gap_seconds integer NOT NULL
);


--
-- Name: feed_gap_id_seq; Type: SEQUENCE; Schema: transit; Owner: -
--

ALTER TABLE transit.feed_gap ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME transit.feed_gap_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: feed_state; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.feed_state (
    feed_type text NOT NULL,
    last_timestamp timestamp with time zone NOT NULL,
    last_fetched_at timestamp with time zone NOT NULL,
    version_hash text
);


--
-- Name: route; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.route (
    route_id text NOT NULL,
    short_name text DEFAULT ''::text NOT NULL,
    long_name text DEFAULT ''::text NOT NULL,
    display_name text NOT NULL,
    route_type integer,
    color text DEFAULT ''::text NOT NULL,
    text_color text DEFAULT ''::text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL
);


--
-- Name: route_baseline; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.route_baseline (
    route_id text NOT NULL,
    service_kind text NOT NULL,
    band text NOT NULL,
    scheduled_trip_count integer NOT NULL,
    scheduled_headway_sec integer NOT NULL,
    sample_n integer NOT NULL,
    CONSTRAINT route_baseline_band_check CHECK ((band = ANY (ARRAY['morning'::text, 'midday'::text, 'evening'::text]))),
    CONSTRAINT route_baseline_service_kind_check CHECK ((service_kind = ANY (ARRAY['weekday'::text, 'saturday'::text, 'sunday'::text])))
);


--
-- Name: route_pattern; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.route_pattern (
    pattern_id text NOT NULL,
    route_id text NOT NULL,
    headsign text DEFAULT ''::text NOT NULL,
    direction_id integer DEFAULT 0 NOT NULL,
    stop_count integer NOT NULL,
    timepoint_count integer NOT NULL
);


--
-- Name: route_pattern_stop; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.route_pattern_stop (
    pattern_id text NOT NULL,
    sequence integer NOT NULL,
    stop_id text NOT NULL,
    is_timepoint boolean DEFAULT false NOT NULL
);


--
-- Name: scheduled_stop; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.scheduled_stop (
    trip_id text NOT NULL,
    stop_sequence integer NOT NULL,
    stop_id text NOT NULL,
    scheduled_arrival text,
    scheduled_departure text,
    is_timepoint boolean DEFAULT false NOT NULL,
    route_id text NOT NULL,
    pattern_id text NOT NULL,
    service_id text NOT NULL,
    headsign text DEFAULT ''::text NOT NULL
);


--
-- Name: service_calendar; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.service_calendar (
    service_id text NOT NULL,
    date date NOT NULL,
    service_kind text NOT NULL,
    CONSTRAINT service_calendar_service_kind_check CHECK ((service_kind = ANY (ARRAY['weekday'::text, 'saturday'::text, 'sunday'::text])))
);


--
-- Name: stop; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.stop (
    stop_id text NOT NULL,
    name text NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    geog public.geography(Point,4326),
    parent_station text DEFAULT ''::text NOT NULL,
    wheelchair boolean DEFAULT false NOT NULL,
    is_terminal boolean DEFAULT false NOT NULL,
    is_transfer boolean DEFAULT false NOT NULL
);


--
-- Name: stop_delay; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.stop_delay (
    date date NOT NULL,
    trip_id text NOT NULL,
    stop_id text NOT NULL,
    stop_sequence integer,
    arrival_delay integer,
    departure_delay integer,
    last_updated timestamp with time zone DEFAULT now() NOT NULL,
    route_id text NOT NULL,
    pattern_id text NOT NULL,
    service_id text NOT NULL,
    service_kind text NOT NULL,
    headsign text DEFAULT ''::text NOT NULL,
    band text NOT NULL,
    is_first_stop boolean NOT NULL,
    is_timepoint boolean DEFAULT false NOT NULL,
    scheduled_first_dep_time text NOT NULL,
    CONSTRAINT stop_delay_band_check CHECK ((band = ANY (ARRAY['morning'::text, 'midday'::text, 'evening'::text]))),
    CONSTRAINT stop_delay_service_kind_check CHECK ((service_kind = ANY (ARRAY['weekday'::text, 'saturday'::text, 'sunday'::text])))
);


--
-- Name: stop_visit; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.stop_visit (
    trip_id text NOT NULL,
    stop_id text NOT NULL,
    route_id text NOT NULL,
    vehicle_id text NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    distance_m real NOT NULL
);


--
-- Name: trip_catalog; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.trip_catalog (
    trip_id text NOT NULL,
    route_id text NOT NULL,
    pattern_id text NOT NULL,
    service_id text NOT NULL,
    service_kind text NOT NULL,
    headsign text DEFAULT ''::text NOT NULL,
    direction_id integer DEFAULT 0 NOT NULL,
    band text NOT NULL,
    scheduled_first_dep_time text NOT NULL,
    scheduled_last_arr_time text NOT NULL,
    block_id text DEFAULT ''::text NOT NULL,
    CONSTRAINT trip_catalog_band_check CHECK ((band = ANY (ARRAY['morning'::text, 'midday'::text, 'evening'::text]))),
    CONSTRAINT trip_catalog_service_kind_check CHECK ((service_kind = ANY (ARRAY['weekday'::text, 'saturday'::text, 'sunday'::text])))
);


--
-- Name: vehicle; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.vehicle (
    vehicle_id text NOT NULL,
    first_seen timestamp with time zone NOT NULL,
    last_seen timestamp with time zone NOT NULL
);


--
-- Name: vehicle_assignment; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.vehicle_assignment (
    date date NOT NULL,
    vehicle_id text NOT NULL,
    trip_id text NOT NULL,
    route_id text NOT NULL,
    started_at timestamp with time zone NOT NULL
);


--
-- Name: vehicle_position; Type: TABLE; Schema: transit; Owner: -
--

CREATE TABLE transit.vehicle_position (
    id bigint NOT NULL,
    feed_timestamp timestamp with time zone NOT NULL,
    vehicle_id text NOT NULL,
    route_id text,
    trip_id text,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    bearing real,
    speed real,
    stop_status text,
    current_stop_id text,
    geog public.geography(Point,4326)
);


--
-- Name: vehicle_position_id_seq; Type: SEQUENCE; Schema: transit; Owner: -
--

ALTER TABLE transit.vehicle_position ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME transit.vehicle_position_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: budget_accounts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_accounts ALTER COLUMN id SET DEFAULT nextval('public.budget_accounts_id_seq'::regclass);


--
-- Name: data_patch_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_patch_log ALTER COLUMN id SET DEFAULT nextval('public.data_patch_log_id_seq'::regclass);


--
-- Name: calendar_dates calendar_dates_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.calendar_dates
    ADD CONSTRAINT calendar_dates_pkey PRIMARY KEY (service_id, date);


--
-- Name: calendar calendar_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.calendar
    ADD CONSTRAINT calendar_pkey PRIMARY KEY (service_id);


--
-- Name: routes routes_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.routes
    ADD CONSTRAINT routes_pkey PRIMARY KEY (route_id);


--
-- Name: shapes shapes_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.shapes
    ADD CONSTRAINT shapes_pkey PRIMARY KEY (shape_id, shape_pt_sequence);


--
-- Name: stop_times stop_times_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.stop_times
    ADD CONSTRAINT stop_times_pkey PRIMARY KEY (trip_id, stop_sequence);


--
-- Name: stops stops_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.stops
    ADD CONSTRAINT stops_pkey PRIMARY KEY (stop_id);


--
-- Name: transfers transfers_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.transfers
    ADD CONSTRAINT transfers_pkey PRIMARY KEY (from_stop_id, to_stop_id);


--
-- Name: trips trips_pkey; Type: CONSTRAINT; Schema: gtfs; Owner: -
--

ALTER TABLE ONLY gtfs.trips
    ADD CONSTRAINT trips_pkey PRIMARY KEY (trip_id);


--
-- Name: budget_accounts budget_accounts_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_accounts
    ADD CONSTRAINT budget_accounts_code_key UNIQUE (code);


--
-- Name: budget_accounts budget_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_accounts
    ADD CONSTRAINT budget_accounts_pkey PRIMARY KEY (id);


--
-- Name: budget_ledger budget_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_ledger
    ADD CONSTRAINT budget_ledger_pkey PRIMARY KEY (id);


--
-- Name: budget_ledger budget_ledger_source_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_ledger
    ADD CONSTRAINT budget_ledger_source_hash_key UNIQUE (source_hash);


--
-- Name: council_meetings council_meetings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_meetings
    ADD CONSTRAINT council_meetings_pkey PRIMARY KEY (id);


--
-- Name: council_motions council_motions_meeting_id_motion_index_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_motions
    ADD CONSTRAINT council_motions_meeting_id_motion_index_key UNIQUE (meeting_id, motion_index);


--
-- Name: council_motions council_motions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_motions
    ADD CONSTRAINT council_motions_pkey PRIMARY KEY (id);


--
-- Name: council_vote_records council_vote_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_vote_records
    ADD CONSTRAINT council_vote_records_pkey PRIMARY KEY (motion_id, councillor);


--
-- Name: councillors councillors_name_term_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.councillors
    ADD CONSTRAINT councillors_name_term_key UNIQUE (name, term);


--
-- Name: councillors councillors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.councillors
    ADD CONSTRAINT councillors_pkey PRIMARY KEY (id);


--
-- Name: data_patch_log data_patch_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.data_patch_log
    ADD CONSTRAINT data_patch_log_pkey PRIMARY KEY (id);


--
-- Name: alert alert_alert_id_feed_timestamp_key; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.alert
    ADD CONSTRAINT alert_alert_id_feed_timestamp_key UNIQUE (alert_id, feed_timestamp);


--
-- Name: alert alert_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.alert
    ADD CONSTRAINT alert_pkey PRIMARY KEY (id);


--
-- Name: cancellation cancellation_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.cancellation
    ADD CONSTRAINT cancellation_pkey PRIMARY KEY (id);


--
-- Name: cancellation cancellation_trip_id_feed_timestamp_key; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.cancellation
    ADD CONSTRAINT cancellation_trip_id_feed_timestamp_key UNIQUE (trip_id, feed_timestamp);


--
-- Name: feed_gap feed_gap_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.feed_gap
    ADD CONSTRAINT feed_gap_pkey PRIMARY KEY (id);


--
-- Name: feed_state feed_state_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.feed_state
    ADD CONSTRAINT feed_state_pkey PRIMARY KEY (feed_type);


--
-- Name: route_baseline route_baseline_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_baseline
    ADD CONSTRAINT route_baseline_pkey PRIMARY KEY (route_id, service_kind, band);


--
-- Name: route_pattern route_pattern_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern
    ADD CONSTRAINT route_pattern_pkey PRIMARY KEY (pattern_id);


--
-- Name: route_pattern route_pattern_route_id_headsign_direction_id_key; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern
    ADD CONSTRAINT route_pattern_route_id_headsign_direction_id_key UNIQUE (route_id, headsign, direction_id);


--
-- Name: route_pattern_stop route_pattern_stop_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern_stop
    ADD CONSTRAINT route_pattern_stop_pkey PRIMARY KEY (pattern_id, sequence);


--
-- Name: route route_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route
    ADD CONSTRAINT route_pkey PRIMARY KEY (route_id);


--
-- Name: scheduled_stop scheduled_stop_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.scheduled_stop
    ADD CONSTRAINT scheduled_stop_pkey PRIMARY KEY (trip_id, stop_sequence);


--
-- Name: service_calendar service_calendar_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.service_calendar
    ADD CONSTRAINT service_calendar_pkey PRIMARY KEY (service_id, date);


--
-- Name: stop_delay stop_delay_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.stop_delay
    ADD CONSTRAINT stop_delay_pkey PRIMARY KEY (date, trip_id, stop_id);


--
-- Name: stop stop_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.stop
    ADD CONSTRAINT stop_pkey PRIMARY KEY (stop_id);


--
-- Name: stop_visit stop_visit_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.stop_visit
    ADD CONSTRAINT stop_visit_pkey PRIMARY KEY (trip_id, stop_id);


--
-- Name: trip_catalog trip_catalog_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.trip_catalog
    ADD CONSTRAINT trip_catalog_pkey PRIMARY KEY (trip_id);


--
-- Name: vehicle_assignment vehicle_assignment_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.vehicle_assignment
    ADD CONSTRAINT vehicle_assignment_pkey PRIMARY KEY (date, vehicle_id, trip_id);


--
-- Name: vehicle vehicle_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.vehicle
    ADD CONSTRAINT vehicle_pkey PRIMARY KEY (vehicle_id);


--
-- Name: vehicle_position vehicle_position_pkey; Type: CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.vehicle_position
    ADD CONSTRAINT vehicle_position_pkey PRIMARY KEY (id);


--
-- Name: idx_gtfs_calendar_dates_date; Type: INDEX; Schema: gtfs; Owner: -
--

CREATE INDEX idx_gtfs_calendar_dates_date ON gtfs.calendar_dates USING btree (date);


--
-- Name: idx_gtfs_stop_times_stop; Type: INDEX; Schema: gtfs; Owner: -
--

CREATE INDEX idx_gtfs_stop_times_stop ON gtfs.stop_times USING btree (stop_id);


--
-- Name: idx_budget_accounts_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_budget_accounts_patch_source ON public.budget_accounts USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_budget_ledger_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_budget_ledger_patch_source ON public.budget_ledger USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_council_motions_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_council_motions_meeting ON public.council_motions USING btree (meeting_id);


--
-- Name: idx_council_motions_search; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_council_motions_search ON public.council_motions USING gin (search_text);


--
-- Name: idx_council_motions_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_council_motions_trgm ON public.council_motions USING gin (motion_text public.gin_trgm_ops);


--
-- Name: idx_councillors_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_councillors_name ON public.councillors USING btree (name);


--
-- Name: idx_councillors_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_councillors_patch_source ON public.councillors USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_councillors_term; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_councillors_term ON public.councillors USING btree (term);


--
-- Name: idx_data_patch_log_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_patch_log_at ON public.data_patch_log USING btree (at);


--
-- Name: idx_data_patch_log_patch_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_data_patch_log_patch_id ON public.data_patch_log USING btree (patch_id, at DESC);


--
-- Name: idx_ledger_credit; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ledger_credit ON public.budget_ledger USING btree (credit_code, fiscal_year);


--
-- Name: idx_ledger_debit; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ledger_debit ON public.budget_ledger USING btree (debit_code, fiscal_year);


--
-- Name: idx_ledger_year; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ledger_year ON public.budget_ledger USING btree (fiscal_year);


--
-- Name: idx_meetings_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_patch_source ON public.council_meetings USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_motions_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_motions_patch_source ON public.council_motions USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_vote_records_patch_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_vote_records_patch_source ON public.council_vote_records USING btree (source) WHERE (source <> 'manual'::text);


--
-- Name: idx_transit_alert_feed_timestamp; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_alert_feed_timestamp ON transit.alert USING btree (feed_timestamp);


--
-- Name: idx_transit_cancellation_feed_timestamp; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_cancellation_feed_timestamp ON transit.cancellation USING btree (feed_timestamp);


--
-- Name: idx_transit_cancellation_route_start; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_cancellation_route_start ON transit.cancellation USING btree (feed_timestamp, trip_id, route_id, start_time, start_date) WHERE (start_time IS NOT NULL);


--
-- Name: idx_transit_route_pattern_route; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_route_pattern_route ON transit.route_pattern USING btree (route_id);


--
-- Name: idx_transit_route_pattern_stop_stop; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_route_pattern_stop_stop ON transit.route_pattern_stop USING btree (stop_id);


--
-- Name: idx_transit_route_pattern_stop_tp; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_route_pattern_stop_tp ON transit.route_pattern_stop USING btree (pattern_id) WHERE (is_timepoint = true);


--
-- Name: idx_transit_scheduled_stop_first_dep; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_scheduled_stop_first_dep ON transit.scheduled_stop USING btree (route_id, scheduled_departure) WHERE (stop_sequence = 1);


--
-- Name: idx_transit_scheduled_stop_stop; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_scheduled_stop_stop ON transit.scheduled_stop USING btree (stop_id);


--
-- Name: idx_transit_scheduled_stop_tp_dep; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_scheduled_stop_tp_dep ON transit.scheduled_stop USING btree (route_id, stop_id, scheduled_departure) WHERE (is_timepoint = true);


--
-- Name: idx_transit_scheduled_stop_trip; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_scheduled_stop_trip ON transit.scheduled_stop USING btree (trip_id);


--
-- Name: idx_transit_service_calendar_date; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_service_calendar_date ON transit.service_calendar USING btree (date);


--
-- Name: idx_transit_stop_delay_first_stop_band; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_delay_first_stop_band ON transit.stop_delay USING btree (date, route_id, band) WHERE (is_first_stop = true);


--
-- Name: idx_transit_stop_delay_last_updated; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_delay_last_updated ON transit.stop_delay USING brin (last_updated) WITH (pages_per_range='32');


--
-- Name: idx_transit_stop_delay_route_stop_date; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_delay_route_stop_date ON transit.stop_delay USING btree (route_id, stop_id, date);


--
-- Name: idx_transit_stop_delay_service_date; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_delay_service_date ON transit.stop_delay USING btree (service_id, date);


--
-- Name: idx_transit_stop_delay_timepoint_band; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_delay_timepoint_band ON transit.stop_delay USING btree (date, route_id, band) WHERE (is_timepoint = true);


--
-- Name: idx_transit_stop_geog; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_geog ON transit.stop USING gist (geog);


--
-- Name: idx_transit_stop_visit_observed; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_visit_observed ON transit.stop_visit USING btree (observed_at);


--
-- Name: idx_transit_stop_visit_route_stop; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_stop_visit_route_stop ON transit.stop_visit USING btree (route_id, stop_id) INCLUDE (observed_at);


--
-- Name: idx_transit_trip_catalog_pattern; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_trip_catalog_pattern ON transit.trip_catalog USING btree (pattern_id);


--
-- Name: idx_transit_trip_catalog_route; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_trip_catalog_route ON transit.trip_catalog USING btree (route_id);


--
-- Name: idx_transit_trip_catalog_service; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_trip_catalog_service ON transit.trip_catalog USING btree (service_id);


--
-- Name: idx_transit_vehicle_position_feed_timestamp; Type: INDEX; Schema: transit; Owner: -
--

CREATE INDEX idx_transit_vehicle_position_feed_timestamp ON transit.vehicle_position USING btree (feed_timestamp);


--
-- Name: stop trg_transit_stop_geog; Type: TRIGGER; Schema: transit; Owner: -
--

CREATE TRIGGER trg_transit_stop_geog BEFORE INSERT OR UPDATE OF latitude, longitude ON transit.stop FOR EACH ROW EXECUTE FUNCTION transit.set_geog_from_latlon();


--
-- Name: vehicle_position trg_transit_vehicle_position_geog; Type: TRIGGER; Schema: transit; Owner: -
--

CREATE TRIGGER trg_transit_vehicle_position_geog BEFORE INSERT OR UPDATE OF latitude, longitude ON transit.vehicle_position FOR EACH ROW EXECUTE FUNCTION transit.set_geog_from_latlon();


--
-- Name: budget_ledger budget_ledger_credit_code_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_ledger
    ADD CONSTRAINT budget_ledger_credit_code_fkey FOREIGN KEY (credit_code) REFERENCES public.budget_accounts(code);


--
-- Name: budget_ledger budget_ledger_debit_code_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_ledger
    ADD CONSTRAINT budget_ledger_debit_code_fkey FOREIGN KEY (debit_code) REFERENCES public.budget_accounts(code);


--
-- Name: council_motions council_motions_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_motions
    ADD CONSTRAINT council_motions_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.council_meetings(id) ON DELETE CASCADE;


--
-- Name: council_vote_records council_vote_records_motion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.council_vote_records
    ADD CONSTRAINT council_vote_records_motion_id_fkey FOREIGN KEY (motion_id) REFERENCES public.council_motions(id) ON DELETE CASCADE;


--
-- Name: route_baseline route_baseline_route_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_baseline
    ADD CONSTRAINT route_baseline_route_id_fkey FOREIGN KEY (route_id) REFERENCES transit.route(route_id) ON DELETE CASCADE;


--
-- Name: route_pattern route_pattern_route_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern
    ADD CONSTRAINT route_pattern_route_id_fkey FOREIGN KEY (route_id) REFERENCES transit.route(route_id) ON DELETE CASCADE;


--
-- Name: route_pattern_stop route_pattern_stop_pattern_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern_stop
    ADD CONSTRAINT route_pattern_stop_pattern_id_fkey FOREIGN KEY (pattern_id) REFERENCES transit.route_pattern(pattern_id) ON DELETE CASCADE;


--
-- Name: route_pattern_stop route_pattern_stop_stop_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.route_pattern_stop
    ADD CONSTRAINT route_pattern_stop_stop_id_fkey FOREIGN KEY (stop_id) REFERENCES transit.stop(stop_id);


--
-- Name: scheduled_stop scheduled_stop_pattern_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.scheduled_stop
    ADD CONSTRAINT scheduled_stop_pattern_id_fkey FOREIGN KEY (pattern_id) REFERENCES transit.route_pattern(pattern_id) ON DELETE CASCADE;


--
-- Name: scheduled_stop scheduled_stop_route_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.scheduled_stop
    ADD CONSTRAINT scheduled_stop_route_id_fkey FOREIGN KEY (route_id) REFERENCES transit.route(route_id) ON DELETE CASCADE;


--
-- Name: scheduled_stop scheduled_stop_stop_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.scheduled_stop
    ADD CONSTRAINT scheduled_stop_stop_id_fkey FOREIGN KEY (stop_id) REFERENCES transit.stop(stop_id);


--
-- Name: trip_catalog trip_catalog_pattern_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.trip_catalog
    ADD CONSTRAINT trip_catalog_pattern_id_fkey FOREIGN KEY (pattern_id) REFERENCES transit.route_pattern(pattern_id) ON DELETE CASCADE;


--
-- Name: trip_catalog trip_catalog_route_id_fkey; Type: FK CONSTRAINT; Schema: transit; Owner: -
--

ALTER TABLE ONLY transit.trip_catalog
    ADD CONSTRAINT trip_catalog_route_id_fkey FOREIGN KEY (route_id) REFERENCES transit.route(route_id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--


