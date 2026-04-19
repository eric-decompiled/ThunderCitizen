-- Sum of rows across all datasets in a pack. Populated on every Apply
-- from BOD's per-dataset row counts. Drives the /data treemap tile size.
ALTER TABLE public.muni_packs
    ADD COLUMN IF NOT EXISTS total_rows bigint NOT NULL DEFAULT 0;
