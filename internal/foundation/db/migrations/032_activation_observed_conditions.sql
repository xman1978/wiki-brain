-- Observed condition tuples for ActivationLink Match
-- (docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md).
ALTER TABLE activation_links ADD COLUMN observed_conditions TEXT NOT NULL DEFAULT '[]';
