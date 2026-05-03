-- Migration 065: Field of Science taxonomy (Sprint E, E2, CF-16).
--
-- Introduces the NSF Field of Science classification taxonomy.
-- node_groups gains a nullable field_of_science_id foreign key.
-- The seed data covers the NSF FOS standard taxonomy (~250 entries with parent hierarchy).

CREATE TABLE IF NOT EXISTS fields_of_science (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    parent_id  TEXT REFERENCES fields_of_science(id),
    nsf_code   TEXT,         -- optional NSF numeric code
    enabled    INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fos_parent ON fields_of_science(parent_id);
CREATE INDEX IF NOT EXISTS idx_fos_enabled ON fields_of_science(enabled);

-- Add field_of_science_id to node_groups (nullable).
ALTER TABLE node_groups ADD COLUMN field_of_science_id TEXT REFERENCES fields_of_science(id);

-- ─── NSF Field of Science Seed Data ──────────────────────────────────────────
-- Source: NSF FOS list as documented in the NSF Survey of Earned Doctorates (SED)
-- and the Higher Education Research and Development (HERD) survey taxonomy.
-- Reference: https://www.nsf.gov/statistics/srvyherd/
-- Note: IDs are stable slugs so admins can safely reference them.

-- Top-level fields (parent_id IS NULL)
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-cs',         'Computer and Information Sciences',            NULL, '11', 10),
('fos-eng',        'Engineering',                                  NULL, '14', 20),
('fos-math',       'Mathematics and Statistics',                   NULL, '27', 30),
('fos-phys',       'Physical Sciences',                            NULL, '40', 40),
('fos-bio',        'Biological Sciences',                          NULL, '26', 50),
('fos-chem',       'Chemistry',                                    NULL, '40.05', 60),
('fos-geo',        'Geosciences, Atmospheric, and Ocean Sciences', NULL, '40.06', 70),
('fos-soc',        'Social Sciences',                              NULL, '45', 80),
('fos-med',        'Medical and Health Sciences',                  NULL, '51', 90),
('fos-agri',       'Agricultural Sciences',                        NULL, '01', 100),
('fos-env',        'Environmental Sciences',                       NULL, '03', 110),
('fos-psy',        'Psychology',                                   NULL, '42', 120),
('fos-edu',        'Education',                                    NULL, '13', 130),
('fos-biz',        'Business and Management',                      NULL, '52', 140),
('fos-law',        'Law',                                          NULL, '22', 150),
('fos-hum',        'Humanities',                                   NULL, '23', 160),
('fos-arts',       'Arts',                                         NULL, '50', 170),
('fos-other',      'Other Sciences',                               NULL, '99', 180);

-- Computer and Information Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-cs-ai',      'Artificial Intelligence',                      'fos-cs', '11.01', 1010),
('fos-cs-ml',      'Machine Learning',                             'fos-cs', '11.02', 1020),
('fos-cs-hpc',     'High Performance Computing',                   'fos-cs', '11.03', 1030),
('fos-cs-net',     'Networking and Communications',                'fos-cs', '11.04', 1040),
('fos-cs-sec',     'Cybersecurity',                                'fos-cs', '11.05', 1050),
('fos-cs-data',    'Data Science and Big Data',                    'fos-cs', '11.06', 1060),
('fos-cs-db',      'Database Systems',                             'fos-cs', '11.07', 1070),
('fos-cs-se',      'Software Engineering',                         'fos-cs', '11.08', 1080),
('fos-cs-vis',     'Computer Vision',                              'fos-cs', '11.09', 1090),
('fos-cs-nlp',     'Natural Language Processing',                  'fos-cs', '11.10', 1100),
('fos-cs-bio',     'Bioinformatics and Computational Biology',     'fos-cs', '11.11', 1110),
('fos-cs-arch',    'Computer Architecture',                        'fos-cs', '11.12', 1120),
('fos-cs-os',      'Operating Systems',                            'fos-cs', '11.13', 1130),
('fos-cs-tcs',     'Theory of Computation',                        'fos-cs', '11.14', 1140),
('fos-cs-hci',     'Human-Computer Interaction',                   'fos-cs', '11.15', 1150),
('fos-cs-dist',    'Distributed Systems',                          'fos-cs', '11.16', 1160),
('fos-cs-cloud',   'Cloud Computing',                              'fos-cs', '11.17', 1170),
('fos-cs-quant',   'Quantum Computing',                            'fos-cs', '11.18', 1180),
('fos-cs-robot',   'Robotics',                                     'fos-cs', '11.19', 1190),
('fos-cs-graph',   'Computer Graphics',                            'fos-cs', '11.20', 1200);

-- Engineering subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-eng-ae',     'Aerospace Engineering',                        'fos-eng', '14.02', 2010),
('fos-eng-bme',    'Biomedical Engineering',                       'fos-eng', '14.05', 2020),
('fos-eng-che',    'Chemical Engineering',                         'fos-eng', '14.07', 2030),
('fos-eng-cve',    'Civil Engineering',                            'fos-eng', '14.08', 2040),
('fos-eng-ee',     'Electrical Engineering',                       'fos-eng', '14.10', 2050),
('fos-eng-env',    'Environmental Engineering',                    'fos-eng', '14.14', 2060),
('fos-eng-ind',    'Industrial Engineering',                       'fos-eng', '14.17', 2070),
('fos-eng-mat',    'Materials Engineering',                        'fos-eng', '14.18', 2080),
('fos-eng-me',     'Mechanical Engineering',                       'fos-eng', '14.19', 2090),
('fos-eng-nuc',    'Nuclear Engineering',                          'fos-eng', '14.23', 2100),
('fos-eng-pet',    'Petroleum Engineering',                        'fos-eng', '14.25', 2110),
('fos-eng-sys',    'Systems Engineering',                          'fos-eng', '14.27', 2120),
('fos-eng-ece',    'Electrical and Computer Engineering',          'fos-eng', '14.10', 2130);

-- Mathematics and Statistics subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-math-pure',  'Pure Mathematics',                             'fos-math', '27.01', 3010),
('fos-math-app',   'Applied Mathematics',                          'fos-math', '27.03', 3020),
('fos-math-stat',  'Statistics',                                   'fos-math', '27.05', 3030),
('fos-math-prob',  'Probability and Stochastic Analysis',          'fos-math', '27.07', 3040),
('fos-math-num',   'Numerical Analysis and Scientific Computing',  'fos-math', '27.09', 3050),
('fos-math-opt',   'Optimization',                                 'fos-math', '27.11', 3060),
('fos-math-disc',  'Discrete Mathematics',                         'fos-math', '27.13', 3070),
('fos-math-alg',   'Algebra',                                      'fos-math', '27.15', 3080),
('fos-math-geo2',  'Geometry and Topology',                        'fos-math', '27.17', 3090),
('fos-math-logic', 'Mathematical Logic and Foundations',           'fos-math', '27.19', 3100);

-- Physical Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-phys-ast',   'Astronomy and Astrophysics',                   'fos-phys', '40.02', 4010),
('fos-phys-atm',   'Atmospheric Physics',                          'fos-phys', '40.04', 4020),
('fos-phys-cond',  'Condensed Matter Physics',                     'fos-phys', '40.08', 4030),
('fos-phys-hep',   'High Energy Physics (Particle Physics)',       'fos-phys', '40.09', 4040),
('fos-phys-nuc',   'Nuclear Physics',                              'fos-phys', '40.10', 4050),
('fos-phys-opt',   'Optics and Photonics',                         'fos-phys', '40.12', 4060),
('fos-phys-plasm', 'Plasma Physics',                               'fos-phys', '40.13', 4070),
('fos-phys-qm',    'Quantum Physics',                              'fos-phys', '40.14', 4080),
('fos-phys-bio',   'Biophysics',                                   'fos-phys', '40.16', 4090),
('fos-phys-cos',   'Cosmology',                                    'fos-phys', '40.17', 4100),
('fos-phys-geo3',  'Geophysics',                                   'fos-phys', '40.06', 4110);

-- Chemistry subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-chem-anal',  'Analytical Chemistry',                         'fos-chem', '40.05.01', 5010),
('fos-chem-bio',   'Biochemistry',                                 'fos-chem', '40.05.02', 5020),
('fos-chem-comp',  'Computational Chemistry',                      'fos-chem', '40.05.03', 5030),
('fos-chem-inorg', 'Inorganic Chemistry',                          'fos-chem', '40.05.04', 5040),
('fos-chem-mat',   'Materials Chemistry',                          'fos-chem', '40.05.05', 5050),
('fos-chem-org',   'Organic Chemistry',                            'fos-chem', '40.05.06', 5060),
('fos-chem-phys',  'Physical Chemistry',                           'fos-chem', '40.05.07', 5070),
('fos-chem-poly',  'Polymer Chemistry',                            'fos-chem', '40.05.08', 5080),
('fos-chem-theo',  'Theoretical Chemistry',                        'fos-chem', '40.05.09', 5090);

-- Biological Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-bio-cell',   'Cell Biology',                                 'fos-bio', '26.02', 6010),
('fos-bio-ecol',   'Ecology',                                      'fos-bio', '26.08', 6020),
('fos-bio-evo',    'Evolutionary Biology',                         'fos-bio', '26.13', 6030),
('fos-bio-gen',    'Genetics and Genomics',                        'fos-bio', '26.04', 6040),
('fos-bio-imm',    'Immunology',                                   'fos-bio', '26.09', 6050),
('fos-bio-mb',     'Microbiology',                                 'fos-bio', '26.05', 6060),
('fos-bio-mol',    'Molecular Biology',                            'fos-bio', '26.07', 6070),
('fos-bio-neuro',  'Neuroscience',                                 'fos-bio', '26.15', 6080),
('fos-bio-plant',  'Plant Biology',                                'fos-bio', '26.10', 6090),
('fos-bio-struct', 'Structural Biology',                           'fos-bio', '26.06', 6100),
('fos-bio-sys',    'Systems Biology',                              'fos-bio', '26.20', 6110),
('fos-bio-marine', 'Marine Biology',                               'fos-bio', '26.12', 6120);

-- Geosciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-geo-atm',    'Atmospheric Science',                          'fos-geo', '40.06.01', 7010),
('fos-geo-clim',   'Climate Science',                              'fos-geo', '40.06.02', 7020),
('fos-geo-geo4',   'Geology',                                      'fos-geo', '40.06.03', 7030),
('fos-geo-hydro',  'Hydrology',                                    'fos-geo', '40.06.04', 7040),
('fos-geo-ocean',  'Oceanography',                                 'fos-geo', '40.06.05', 7050),
('fos-geo-remote', 'Remote Sensing',                               'fos-geo', '40.06.06', 7060),
('fos-geo-seis',   'Seismology',                                   'fos-geo', '40.06.07', 7070),
('fos-geo-soil',   'Soil Science',                                 'fos-geo', '40.06.08', 7080);

-- Medical and Health Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-med-clin',   'Clinical Medicine',                            'fos-med', '51.01', 8010),
('fos-med-epi',    'Epidemiology',                                 'fos-med', '51.02', 8020),
('fos-med-gen',    'Medical Genetics',                             'fos-med', '51.03', 8030),
('fos-med-imag',   'Medical Imaging',                              'fos-med', '51.04', 8040),
('fos-med-pharm',  'Pharmacology',                                 'fos-med', '51.05', 8050),
('fos-med-pub',    'Public Health',                                 'fos-med', '51.06', 8060),
('fos-med-rad',    'Radiology',                                    'fos-med', '51.07', 8070),
('fos-med-bio',    'Medical Bioinformatics',                       'fos-med', '51.08', 8080);

-- Social Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-soc-econ',   'Economics',                                    'fos-soc', '45.06', 9010),
('fos-soc-pol',    'Political Science',                            'fos-soc', '45.10', 9020),
('fos-soc-psych2', 'Social Psychology',                            'fos-soc', '45.11', 9030),
('fos-soc-soc',    'Sociology',                                    'fos-soc', '45.13', 9040),
('fos-soc-anthro', 'Anthropology',                                 'fos-soc', '45.02', 9050),
('fos-soc-geo5',   'Human Geography',                              'fos-soc', '45.07', 9060),
('fos-soc-demo',   'Demography',                                   'fos-soc', '45.04', 9070),
('fos-soc-comms',  'Communication Studies',                        'fos-soc', '45.03', 9080);

-- Agricultural Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-agri-crop',  'Crop Science',                                 'fos-agri', '01.01', 10010),
('fos-agri-food',  'Food Science',                                 'fos-agri', '01.09', 10020),
('fos-agri-soil2', 'Soil and Water Science',                       'fos-agri', '01.12', 10030),
('fos-agri-vet',   'Veterinary Science',                           'fos-agri', '01.15', 10040),
('fos-agri-forest','Forestry',                                     'fos-agri', '01.05', 10050);

-- Environmental Sciences subfields
INSERT OR IGNORE INTO fields_of_science (id, name, parent_id, nsf_code, sort_order) VALUES
('fos-env-cons',   'Conservation Biology',                         'fos-env', '03.01', 11010),
('fos-env-sustain','Sustainability Science',                        'fos-env', '03.02', 11020),
('fos-env-waste',  'Environmental Engineering and Waste',          'fos-env', '03.03', 11030),
('fos-env-tox',    'Toxicology',                                   'fos-env', '03.04', 11040);
