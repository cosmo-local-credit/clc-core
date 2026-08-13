-- Reports: normalize the editor body out of JSONB (MIGRATION.md §4.8).
--
-- Before this migration `reports.body` held the raw Plate/ProseMirror document
-- and GET /api/v2/reports/:id re-derived three separate facts from it on every
-- request, with two independent `jsonb_array_elements(r.body)` passes: the
-- image list, and the field-report form. Measured on the imported catalog that
-- costs 0.47 ms for a typical row and 15 ms for the worst, and it re-reads
-- 14 MB of base64 that the query then deliberately discards.
--
-- Measured shape of the 4,632 imported rows:
--
--   * 3,724 of 3,730 approved bodies are under 7.5 KB. Five rows hold 14 MB of
--     the 17 MB total, all of it base64 `data:` URIs in legacy `img` nodes.
--   * 6,571 image nodes carry an http(s) URL. Every (report, url) pair is
--     unique, so (report_id, url) is a real second candidate key.
--   * 48 `cloud_image` nodes still hold unresolved "#nanoid" editor upload
--     placeholders, and 4 `img` nodes have no url at all. All point at nothing.
--   * Only 310 rows have any prose at all (129 KB); the other 4,322 carry
--     empty text nodes and keep their content in `description` and the form.
--   * `location_name` is NULL on all 4,632 rows — it was never populated.
--
-- Body becomes markdown TEXT rather than plain text so the 41 hyperlinks whose
-- URL exists only in the node attribute survive, along with heading levels,
-- lists and the one table. Parsing moves to write time: these functions are
-- the single parser, called by this migration and by the federation importer.

-- ── parser ──────────────────────────────────────────────────────────────────
--
-- Every emptiness test trims E' \t\r\n\f\v' explicitly. Bare btrim() removes
-- spaces only, so a node holding just a newline reads as non-empty and its
-- whitespace ends up duplicated around the emphasis markers.

CREATE OR REPLACE FUNCTION report_md_inline(node JSONB)
RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    ws      CONSTANT TEXT := E' \t\r\n\f\v';
    child   JSONB;
    acc     TEXT := '';
    txt     TEXT;
    core    TEXT;
    lead    TEXT;
    trail   TEXT;
    is_bold BOOLEAN;
    is_ital BOOLEAN;
BEGIN
    IF node IS NULL OR jsonb_typeof(node) <> 'object' THEN
        RETURN '';
    END IF;

    IF node ? 'text' THEN
        txt := node->>'text';
        IF txt IS NULL OR txt = '' THEN
            RETURN '';
        END IF;

        core := btrim(txt, ws);
        -- Whitespace-only: pass through untouched, never wrap in markers.
        IF core = '' THEN
            RETURN txt;
        END IF;
        lead  := substring(txt from '^[[:space:]]*');
        trail := substring(txt from '[[:space:]]*$');

        IF COALESCE((node->>'code')::BOOLEAN, false) THEN
            core := '`' || core || '`';
        END IF;

        is_bold := COALESCE((node->>'bold')::BOOLEAN, false);
        is_ital := COALESCE((node->>'italic')::BOOLEAN, false);
        IF is_bold AND is_ital THEN
            core := '***' || core || '***';
        ELSIF is_bold THEN
            core := '**' || core || '**';
        ELSIF is_ital THEN
            core := '*' || core || '*';
        END IF;

        -- Markers sit inside the surrounding whitespace: "**text **" is not
        -- emphasis in any markdown reader.
        RETURN lead || core || trail;
    END IF;

    FOR child IN
        SELECT value FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(node->'children') = 'array'
                 THEN node->'children' ELSE '[]'::JSONB END)
    LOOP
        acc := acc || report_md_inline(child);
    END LOOP;

    -- A link's URL lives only in the node attribute and the visible text is
    -- often a bare label ("cecb8ab"), so flattening without this loses the
    -- destination entirely.
    IF node->>'type' = 'a'
       AND NULLIF(btrim(COALESCE(node->>'url', ''), ws), '') IS NOT NULL THEN
        IF btrim(acc, ws) = '' THEN
            acc := node->>'url';
        END IF;
        RETURN '[' || btrim(acc, ws) || '](' || btrim(node->>'url', ws) || ')';
    END IF;

    RETURN acc;
END;
$$;

-- One bullet per `li`; nested lists indent by two spaces.
CREATE OR REPLACE FUNCTION report_md_list(node JSONB, depth INT)
RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    ws       CONSTANT TEXT := E' \t\r\n\f\v';
    item     JSONB;
    sub      JSONB;
    lines    TEXT[] := '{}';
    marker   TEXT;
    pad      TEXT := repeat('  ', depth);
    itemtext TEXT;
    nested   TEXT;
    idx      INT := 0;
BEGIN
    IF depth > 5 THEN
        RETURN '';
    END IF;

    FOR item IN
        SELECT value FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(node->'children') = 'array'
                 THEN node->'children' ELSE '[]'::JSONB END)
    LOOP
        idx := idx + 1;
        marker := CASE WHEN node->>'type' = 'ol' THEN idx || '. ' ELSE '- ' END;

        itemtext := '';
        nested   := '';
        FOR sub IN
            SELECT value FROM jsonb_array_elements(
                CASE WHEN jsonb_typeof(item->'children') = 'array'
                     THEN item->'children' ELSE '[]'::JSONB END)
        LOOP
            IF sub->>'type' IN ('ul', 'ol') THEN
                nested := nested || report_md_list(sub, depth + 1);
            ELSE
                itemtext := itemtext || report_md_inline(sub);
            END IF;
        END LOOP;

        IF btrim(itemtext, ws) <> '' THEN
            lines := lines || (pad || marker || btrim(itemtext, ws));
        END IF;
        IF btrim(nested, ws) <> '' THEN
            lines := lines || nested;
        END IF;
    END LOOP;

    RETURN array_to_string(lines, E'\n');
END;
$$;

-- One block node. Media and form nodes return NULL: after this migration they
-- have their own relations and columns, so leaving them in the prose would
-- store the same fact twice.
CREATE OR REPLACE FUNCTION report_md_block(node JSONB)
RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    ws      CONSTANT TEXT := E' \t\r\n\f\v';
    kind    TEXT;
    body    TEXT;
    trow    JSONB;
    cells   TEXT[];
    lines   TEXT[] := '{}';
    isfirst BOOLEAN := true;
BEGIN
    IF node IS NULL OR jsonb_typeof(node) <> 'object' THEN
        RETURN NULL;
    END IF;
    kind := COALESCE(node->>'type', 'p');

    IF kind IN ('cloud_image', 'img', 'cloud_attachment', 'media_embed',
                'field-report-form') THEN
        RETURN NULL;
    END IF;

    IF kind = 'hr' THEN
        RETURN '---';
    END IF;

    IF kind IN ('ul', 'ol') THEN
        RETURN NULLIF(report_md_list(node, 0), '');
    END IF;

    IF kind = 'code_block' THEN
        body := array_to_string(ARRAY(
            SELECT report_md_inline(line)
            FROM jsonb_array_elements(
                CASE WHEN jsonb_typeof(node->'children') = 'array'
                     THEN node->'children' ELSE '[]'::JSONB END) line
        ), E'\n');
        IF btrim(body, ws) = '' THEN
            RETURN NULL;
        END IF;
        RETURN E'```\n' || body || E'\n```';
    END IF;

    IF kind = 'table' THEN
        FOR trow IN
            SELECT value FROM jsonb_array_elements(
                CASE WHEN jsonb_typeof(node->'children') = 'array'
                     THEN node->'children' ELSE '[]'::JSONB END)
        LOOP
            cells := ARRAY(
                SELECT btrim(replace(report_md_inline(c), '|', '\|'), ws)
                FROM jsonb_array_elements(
                    CASE WHEN jsonb_typeof(trow->'children') = 'array'
                         THEN trow->'children' ELSE '[]'::JSONB END) c
            );
            IF array_length(cells, 1) IS NULL THEN
                CONTINUE;
            END IF;
            lines := lines || ('| ' || array_to_string(cells, ' | ') || ' |');
            -- Without the delimiter row the table renders as prose.
            IF isfirst THEN
                lines := lines || ('|' || repeat(' --- |', array_length(cells, 1)));
                isfirst := false;
            END IF;
        END LOOP;
        RETURN NULLIF(array_to_string(lines, E'\n'), '');
    END IF;

    body := btrim(report_md_inline(node), ws);
    IF body = '' THEN
        RETURN NULL;
    END IF;

    IF kind ~ '^h[1-6]$' THEN
        RETURN repeat('#', substring(kind from 2)::INT) || ' ' || body;
    END IF;

    IF kind = 'blockquote' THEN
        RETURN '> ' || replace(body, E'\n', E'\n> ');
    END IF;

    RETURN body;
END;
$$;

-- Whole document, blocks separated by a blank line so the result is valid
-- markdown rather than run-together prose.
CREATE OR REPLACE FUNCTION report_body_markdown(body JSONB)
RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(string_agg(md, E'\n\n' ORDER BY ord), '')
    FROM (
        SELECT ord, report_md_block(node) AS md
        FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(body) = 'array' THEN body ELSE '[]'::JSONB END
        ) WITH ORDINALITY AS t(node, ord)
    ) blocks
    WHERE md IS NOT NULL AND btrim(md, E' \t\r\n\f\v') <> '';
$$;

-- Image nodes in document order. Only http(s) survives: the base64 `data:`
-- URIs reach ~900 KB each and must never enter a response, and the "#nanoid"
-- placeholders resolve to nothing.
CREATE OR REPLACE FUNCTION report_body_images(body JSONB)
RETURNS TABLE (
    ordinal    INT,
    url        TEXT,
    width      INT,
    height     INT,
    max_width  INT,
    max_height INT,
    byte_size  BIGINT,
    caption    TEXT
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (row_number() OVER (ORDER BY ord))::INT,
        btrim(node->>'url', E' \t\r\n\f\v'),
        NULLIF(node->>'width',     '')::NUMERIC::INT,
        NULLIF(node->>'height',    '')::NUMERIC::INT,
        NULLIF(node->>'maxWidth',  '')::NUMERIC::INT,
        NULLIF(node->>'maxHeight', '')::NUMERIC::INT,
        NULLIF(node->>'bytes',     '')::NUMERIC::BIGINT,
        -- `caption` is itself a rich-text array, not a string.
        NULLIF(btrim(report_md_inline(jsonb_build_object('children', node->'caption')),
                     E' \t\r\n\f\v'), '')
    FROM jsonb_array_elements(
        CASE WHEN jsonb_typeof(body) = 'array' THEN body ELSE '[]'::JSONB END
    ) WITH ORDINALITY AS t(node, ord)
    WHERE node->>'type' IN ('cloud_image', 'img')
        AND btrim(COALESCE(node->>'url', ''), E' \t\r\n\f\v') ~* '^https?://';
$$;

-- Non-image media, in document order: uploaded files and external embeds.
CREATE OR REPLACE FUNCTION report_body_attachments(body JSONB)
RETURNS TABLE (
    ordinal   INT,
    kind      TEXT,
    url       TEXT,
    filename  TEXT,
    byte_size BIGINT
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (row_number() OVER (ORDER BY ord))::INT,
        CASE WHEN node->>'type' = 'media_embed' THEN 'EMBED' ELSE 'FILE' END,
        btrim(node->>'url', E' \t\r\n\f\v'),
        NULLIF(btrim(COALESCE(node->>'filename', ''), E' \t\r\n\f\v'), ''),
        NULLIF(node->>'bytes', '')::NUMERIC::BIGINT
    FROM jsonb_array_elements(
        CASE WHEN jsonb_typeof(body) = 'array' THEN body ELSE '[]'::JSONB END
    ) WITH ORDINALITY AS t(node, ord)
    WHERE node->>'type' IN ('cloud_attachment', 'media_embed')
        AND btrim(COALESCE(node->>'url', ''), E' \t\r\n\f\v') ~* '^https?://';
$$;

-- The field-report form. Seven rows carry two form nodes and on two of those
-- the second differs, so the rule is explicit rather than an unordered
-- LIMIT 1: take the first node in document order that has any value at all.
-- `formData.description` is deliberately not read — it equals
-- reports.description on 4,505 rows and is empty on the other 127, so the
-- column is already its single home.
CREATE OR REPLACE FUNCTION report_body_form(body JSONB)
RETURNS TABLE (host_name TEXT, participants TEXT, time_taken TEXT)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT f.host_name, f.participants, f.time_taken
    FROM (
        SELECT ord,
            NULLIF(btrim(COALESCE(node->'formData'->>'hostName',     ''), E' \t\r\n\f\v'), '') AS host_name,
            NULLIF(btrim(COALESCE(node->'formData'->>'participants', ''), E' \t\r\n\f\v'), '') AS participants,
            NULLIF(btrim(COALESCE(node->'formData'->>'timeTaken',    ''), E' \t\r\n\f\v'), '') AS time_taken
        FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(body) = 'array' THEN body ELSE '[]'::JSONB END
        ) WITH ORDINALITY AS t(node, ord)
        WHERE node->>'type' = 'field-report-form'
    ) f
    WHERE f.host_name IS NOT NULL
       OR f.participants IS NOT NULL
       OR f.time_taken IS NOT NULL
    ORDER BY f.ord
    LIMIT 1;
$$;

-- ── schema ──────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS report_media_kind (
    value TEXT PRIMARY KEY
);
INSERT INTO report_media_kind (value) VALUES
    ('FILE'),
    ('EMBED')
ON CONFLICT (value) DO NOTHING;

-- One row per image occurrence. Two candidate keys hold on the live data:
-- (report_id, ordinal) is document order, and (report_id, url) is unique
-- across all 6,571 rows — no report references the same image twice. Both are
-- declared, so the relation is in BCNF under either.
CREATE TABLE IF NOT EXISTS report_images (
    report_id  INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    ordinal    INT  NOT NULL,
    url        TEXT NOT NULL,
    -- The editor's display box. NOT the intrinsic size: 267x1000 display
    -- against 1600x747 intrinsic occurs, so these are not a scaled pair.
    width      INT,
    height     INT,
    -- The intrinsic size the editor recorded (`maxWidth` / `maxHeight`).
    max_width  INT,
    max_height INT,
    byte_size  BIGINT,
    caption    TEXT,
    PRIMARY KEY (report_id, ordinal),
    CONSTRAINT report_images_url_unique UNIQUE (report_id, url),
    CONSTRAINT report_images_ordinal_positive CHECK (ordinal > 0),
    -- Storing a base64 data URI here would defeat the whole migration.
    CONSTRAINT report_images_url_http CHECK (url ~* '^https?://'),
    CONSTRAINT report_images_dims_positive CHECK (
        (width      IS NULL OR width      > 0)
        AND (height     IS NULL OR height     > 0)
        AND (max_width  IS NULL OR max_width  > 0)
        AND (max_height IS NULL OR max_height > 0)
    ),
    CONSTRAINT report_images_byte_size_positive
        CHECK (byte_size IS NULL OR byte_size > 0),
    CONSTRAINT report_images_caption_not_blank
        CHECK (caption IS NULL OR btrim(caption, E' \t\r\n\f\v') <> '')
);

-- Uploaded files (PDF, video) and external embeds (YouTube). A separate
-- relation from report_images because the attributes genuinely differ:
-- attachments have a filename and no dimensions, images the reverse. Folding
-- them together would mean columns that never apply to either kind.
CREATE TABLE IF NOT EXISTS report_attachments (
    report_id INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    ordinal   INT  NOT NULL,
    kind      TEXT NOT NULL REFERENCES report_media_kind(value),
    url       TEXT NOT NULL,
    filename  TEXT,
    byte_size BIGINT,
    PRIMARY KEY (report_id, ordinal),
    CONSTRAINT report_attachments_ordinal_positive CHECK (ordinal > 0),
    CONSTRAINT report_attachments_url_http CHECK (url ~* '^https?://'),
    CONSTRAINT report_attachments_byte_size_positive
        CHECK (byte_size IS NULL OR byte_size > 0),
    CONSTRAINT report_attachments_filename_not_blank
        CHECK (filename IS NULL OR btrim(filename, E' \t\r\n\f\v') <> '')
);

-- The field-report form is a single-valued attribute of the report, so it
-- belongs on `reports` (MIGRATION.md §2.1 V6: sparse facts are nullable
-- columns, not a second table). All three are issuer free text and are never
-- normalized — `time_taken` alone holds "2", "1 hour", "2hrs" and "1 hr".
ALTER TABLE reports
    ADD COLUMN IF NOT EXISTS host_name    TEXT,
    ADD COLUMN IF NOT EXISTS participants TEXT,
    ADD COLUMN IF NOT EXISTS time_taken   TEXT;

ALTER TABLE reports
    DROP CONSTRAINT IF EXISTS reports_form_fields_not_blank;
ALTER TABLE reports
    ADD CONSTRAINT reports_form_fields_not_blank CHECK (
        (host_name    IS NULL OR btrim(host_name,    E' \t\r\n\f\v') <> '')
        AND (participants IS NULL OR btrim(participants, E' \t\r\n\f\v') <> '')
        AND (time_taken   IS NULL OR btrim(time_taken,   E' \t\r\n\f\v') <> '')
    );

-- ── data movement ───────────────────────────────────────────────────────────
--
-- Guarded on `body` still being JSONB so a fresh database — where 001 already
-- creates the final shape — runs this migration as a no-op. The statements are
-- dynamic because they would not type-check against a TEXT `body`.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
            AND table_name = 'reports'
            AND column_name = 'body'
            AND data_type = 'jsonb'
    ) THEN
        RETURN;
    END IF;

    EXECUTE $q$
        INSERT INTO report_images (report_id, ordinal, url, width, height,
            max_width, max_height, byte_size, caption)
        SELECT r.id, i.ordinal, i.url, i.width, i.height,
            i.max_width, i.max_height, i.byte_size, i.caption
        FROM reports r
        CROSS JOIN LATERAL report_body_images(r.body) i
        ON CONFLICT DO NOTHING
    $q$;

    EXECUTE $q$
        INSERT INTO report_attachments (report_id, ordinal, kind, url,
            filename, byte_size)
        SELECT r.id, a.ordinal, a.kind, a.url, a.filename, a.byte_size
        FROM reports r
        CROSS JOIN LATERAL report_body_attachments(r.body) a
        ON CONFLICT DO NOTHING
    $q$;

    -- update_reports_timestamp is a BEFORE UPDATE trigger that assigns
    -- current_timestamp, and it overrides any updated_at the UPDATE sets. Left
    -- enabled it would stamp all 4,521 rows carrying a form with the migration's
    -- own run time — and the feed is ordered by updated_at, so those reports
    -- would collapse onto one instant at the top of it and the (updated_at, id)
    -- keyset cursor would lose the distribution it depends on. Backfilling a
    -- derived column is not a modification of the report.
    EXECUTE 'ALTER TABLE reports DISABLE TRIGGER update_reports_timestamp';

    EXECUTE $q$
        UPDATE reports r
        SET host_name    = f.host_name,
            participants = f.participants,
            time_taken   = f.time_taken
        FROM (
            SELECT r2.id, g.host_name, g.participants, g.time_taken
            FROM reports r2
            CROSS JOIN LATERAL report_body_form(r2.body) g
        ) f
        WHERE f.id = r.id
    $q$;

    EXECUTE 'ALTER TABLE reports ENABLE TRIGGER update_reports_timestamp';

    -- ALTER ... TYPE ... USING keeps the column in place, so the upgraded and
    -- freshly created tables end up with identical column order.
    EXECUTE $q$
        ALTER TABLE reports
            ALTER COLUMN body TYPE TEXT USING report_body_markdown(body)
    $q$;
END
$$;

-- ── drops ───────────────────────────────────────────────────────────────────

-- NULL on all 4,632 rows; the source stored geo as a "lat, lng" string and
-- never a place name, so the column only ever held nothing.
ALTER TABLE reports DROP COLUMN IF EXISTS location_name;

-- Dropped by decision, not because it was empty: 25 REJECTED rows carried a
-- moderator note. Rejection review is not a clc-core concern, and REJECTED
-- reports are invisible on both public routes.
ALTER TABLE reports DROP CONSTRAINT IF EXISTS reports_rejection_when_rejected;
ALTER TABLE reports DROP CONSTRAINT IF EXISTS reports_rejection_only_if_rejected;
ALTER TABLE reports DROP COLUMN IF EXISTS rejection_reason;

-- `body` is now markdown, so an empty document is '' rather than NULL.
ALTER TABLE reports ALTER COLUMN body SET DEFAULT '';
