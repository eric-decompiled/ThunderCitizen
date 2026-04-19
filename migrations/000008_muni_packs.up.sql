-- Pack-level registry for muni data bundles. One row per logical
-- (domain, unit) group declared in BOD.tsv — e.g. "budget-2026",
-- "council-2022-2026", "transit-2026-04-19". Per-dataset history
-- continues to live in data_patch_log; this table is the summary
-- view that powers the /data admin page.
CREATE TABLE IF NOT EXISTS public.muni_packs (
    pack_id       text PRIMARY KEY,
    unit_kind     text NOT NULL,
    unit_start    date,
    unit_end      date,
    dataset_count int  NOT NULL,
    signer_fp     text NOT NULL,
    bundle_merkle text NOT NULL,
    applied_at    timestamptz NOT NULL DEFAULT now(),
    last_error    text
);

CREATE INDEX IF NOT EXISTS idx_muni_packs_unit_kind_start
    ON public.muni_packs (unit_kind, unit_start DESC NULLS LAST);

-- Dev-only cache: hash of the last-applied local BOD.tsv. Lets `make
-- dev` skip the apply goroutine entirely when BOD is unchanged since
-- the previous boot. Singleton row (id = 1).
CREATE TABLE IF NOT EXISTS public.muni_dev_cache (
    id              integer PRIMARY KEY DEFAULT 1,
    bod_sha         text NOT NULL DEFAULT '',
    last_applied_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT muni_dev_cache_singleton CHECK (id = 1)
);
