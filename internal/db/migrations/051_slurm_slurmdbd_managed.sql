-- Backfill slurmdbd.conf into managed_files for existing Slurm module configs.
-- Prior to this migration, defaultManagedFiles did not include slurmdbd.conf,
-- so controller nodes never received it and installSlurmInChroot could not
-- detect the controller role (hasSlurmdbd=false) → slurm-slurmctld was never
-- installed on the controller.
--
-- This UPDATE appends "slurmdbd.conf" to every row whose managed_files JSON
-- array does not already contain it.

UPDATE slurm_module_config
SET managed_files = (
    SELECT json_insert(managed_files, '$[#]', 'slurmdbd.conf')
)
WHERE json_type(managed_files) = 'array'
  AND NOT EXISTS (
      SELECT 1
      FROM json_each(managed_files)
      WHERE value = 'slurmdbd.conf'
  );
