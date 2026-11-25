-- Public profiles and the existing public avatar identity are discoverable by
-- default. Additional personal fields and Post authorship remain opt-in;
-- resource sections expose already-public catalog data.
ALTER TABLE users
    ADD COLUMN public_profile_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_avatar BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_name BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_bio BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_website BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_email BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_phone BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_country BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_location_name BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_precise_location BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_preferred_currency BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_vouchers BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_offers BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_wants BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_pools BOOLEAN NOT NULL DEFAULT TRUE,
    -- Post authorship is a distinct privacy choice and never inferred merely
    -- from enabling the profile or its commerce sections.
    ADD COLUMN public_profile_posts BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN public_profile_catalogs BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN public_profile_activity BOOLEAN NOT NULL DEFAULT FALSE;

---- create above / drop below ----

ALTER TABLE users
    DROP COLUMN IF EXISTS public_profile_enabled,
    DROP COLUMN IF EXISTS public_profile_avatar,
    DROP COLUMN IF EXISTS public_profile_name,
    DROP COLUMN IF EXISTS public_profile_bio,
    DROP COLUMN IF EXISTS public_profile_website,
    DROP COLUMN IF EXISTS public_profile_email,
    DROP COLUMN IF EXISTS public_profile_phone,
    DROP COLUMN IF EXISTS public_profile_country,
    DROP COLUMN IF EXISTS public_profile_location_name,
    DROP COLUMN IF EXISTS public_profile_precise_location,
    DROP COLUMN IF EXISTS public_profile_preferred_currency,
    DROP COLUMN IF EXISTS public_profile_vouchers,
    DROP COLUMN IF EXISTS public_profile_offers,
    DROP COLUMN IF EXISTS public_profile_wants,
    DROP COLUMN IF EXISTS public_profile_pools,
    DROP COLUMN IF EXISTS public_profile_posts,
    DROP COLUMN IF EXISTS public_profile_catalogs,
    DROP COLUMN IF EXISTS public_profile_activity;
