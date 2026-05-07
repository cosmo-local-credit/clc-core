-- Replace the predefined tag list with the canonical Grassecon set.
-- Tags referenced by existing token records cannot be deleted (FK constraint),
-- so we only remove unreferenced old tags to keep history intact.
DELETE FROM token_tag
WHERE value NOT IN (
    'Agriculture','Care','Construction','Cleaning','Education','Elders',
    'Endowments','Fellowship','Gathering','Handcrafts','Housekeeping',
    'Lending','Market','Outreach','Software','Tree Planting','Training',
    'Volunteer','Water Catchment','Youth','Other'
)
AND value NOT IN (SELECT DISTINCT tag_value FROM token_deploy_meta_tags)
AND value NOT IN (SELECT DISTINCT tag_value FROM deployed_token_tags);

INSERT INTO token_tag (value) VALUES
    ('Agriculture'),
    ('Care'),
    ('Construction'),
    ('Cleaning'),
    ('Education'),
    ('Elders'),
    ('Endowments'),
    ('Fellowship'),
    ('Gathering'),
    ('Handcrafts'),
    ('Housekeeping'),
    ('Lending'),
    ('Market'),
    ('Outreach'),
    ('Software'),
    ('Tree Planting'),
    ('Training'),
    ('Volunteer'),
    ('Water Catchment'),
    ('Youth'),
    ('Other')
ON CONFLICT (value) DO NOTHING;
