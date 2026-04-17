-- Add condition column to edge_decisions for diagnostic visibility.
ALTER TABLE edge_decisions ADD COLUMN condition TEXT NOT NULL DEFAULT '';
