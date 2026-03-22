-- Re-tag councillor rows that were inserted by the legacy scripts/seed.sql
-- flow (source='manual') so the new 0000_councillors data patch can claim
-- them. After this migration runs, the patch tracker treats them as
-- patch-owned and Down() can roll them back.
--
-- Production fresh installs never see source='manual' for councillors —
-- this is a one-time fixup for dev/staging databases that were seeded
-- before the patches.Apply auto-boot integration landed.
UPDATE councillors SET source = '0000_councillors' WHERE source = 'manual';
