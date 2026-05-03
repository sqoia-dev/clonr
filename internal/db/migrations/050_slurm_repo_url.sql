-- Add slurm_repo_url to slurm_module_config for auto-install at deploy time.
-- When set, finalize.go adds this repo to the node's dnf config and runs
-- `dnf install -y slurm slurm-slurmctld slurm-slurmd munge` inside the chroot
-- before writing Slurm config files.  Null = no auto-install (operator managed).
ALTER TABLE slurm_module_config ADD COLUMN slurm_repo_url TEXT;
