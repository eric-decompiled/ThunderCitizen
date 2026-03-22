-- Drop everything created by 000001_schema.up.sql.
-- CASCADE on the schemas takes care of dependency order and all tables,
-- triggers, functions, indexes, constraints, and sequences inside them.

DROP SCHEMA IF EXISTS gtfs CASCADE;
DROP SCHEMA IF EXISTS transit CASCADE;

-- public objects are dropped individually because we can't drop the public
-- schema itself (initdb creates it and other tooling expects it to exist).

DROP VIEW IF EXISTS public.budget_balance;

DROP FUNCTION IF EXISTS public.check_ledger_balance();
DROP FUNCTION IF EXISTS public.set_geog_from_latlon();

DROP TABLE IF EXISTS public.data_patch_log;
DROP TABLE IF EXISTS public.council_vote_records;
DROP TABLE IF EXISTS public.council_motions;
DROP TABLE IF EXISTS public.council_meetings;
DROP TABLE IF EXISTS public.councillors;
DROP TABLE IF EXISTS public.budget_ledger;
DROP TABLE IF EXISTS public.budget_accounts;

DROP EXTENSION IF EXISTS postgis;
DROP EXTENSION IF EXISTS pg_trgm;
