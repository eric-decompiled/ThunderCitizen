-- Track who signed each data bundle applied via the muni system.
ALTER TABLE public.data_patch_log ADD COLUMN IF NOT EXISTS signer text NOT NULL DEFAULT '';
