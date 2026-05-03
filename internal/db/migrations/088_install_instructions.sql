-- 088_install_instructions: add install_instructions column to base_images.
-- Stores a JSON array of InstallInstruction objects applied during the in-chroot
-- phase of every deploy. Defaults to an empty array so existing image rows
-- deserialise cleanly with zero instructions.
ALTER TABLE base_images ADD COLUMN install_instructions TEXT NOT NULL DEFAULT '[]';
