-- 000004_stop_visit_entry_exit.down.sql
--
-- Reverts 000004_stop_visit_entry_exit.up.sql by dropping the three columns
-- added for the entry/exit/dwell state machine. The tracker code must be
-- reverted in tandem — after rollback, any tracker write referencing these
-- columns will fail.

ALTER TABLE transit.stop_visit
    DROP COLUMN IF EXISTS entered_at,
    DROP COLUMN IF EXISTS exited_at,
    DROP COLUMN IF EXISTS inside_polls;
