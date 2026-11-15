-- Compact labels for admin group badges; existing groups have no custom alias.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS alias VARCHAR(4) NOT NULL DEFAULT '';
