-- +goose Up
-- Compound words (#56, part 2; docs/development/architecture.md, "Search
-- index", Compounds): document_search_tsquery also ORs into a word the
-- lexemes of the search vocabulary (00018) that contain it, so rechnung
-- finds Stromrechnung. The function is the one of 00011 plus that step;
-- everything else is unchanged. Down restores 00011's body.
--
-- The expansion, for a non-phrase item of 4 or more characters
-- (document_search_compound_patterns): the term's one paperless_simple
-- lexeme and, when it differs, its german stem (each of 4 or more
-- characters) are looked up in search_vocabulary within the owner (all
-- owners for NULL) as LIKE '%p%', wildcards escaped, leaving out lexemes
-- that start with a pattern (the word itself and its prefixes match as
-- before). At most 100 lexemes per item, ordered by n DESC, lexeme, and at
-- most 250 per search, shared out fairly (#140): items that find fewer
-- than an equal share keep all of theirs, the rest split what is left, so
-- a single word keeps its 100. Each is quoted by the fixed escaper and
-- ORed into the item before negation, so -X excludes exactly what X
-- finds. The expanded tsquery stays inside the store like the rest: a
-- bound parameter, never logged or returned.

-- document_search_compound_patterns returns the LIKE patterns of a term's
-- expansion, unescaped: its one paperless_simple lexeme of 4 or more
-- characters and its german stem when that differs and has 4 or more
-- characters; NULL when the term does not expand (under 4 characters, or
-- several simple lexemes such as Strom-Rechnung, whose parts match
-- already). It does not look at phrases; the caller skips those.
-- +goose StatementBegin
CREATE FUNCTION document_search_compound_patterns(term text) RETURNS text[]
LANGUAGE plpgsql STABLE AS $$
DECLARE
    lexemes text[];
    stems text[];
BEGIN
    IF term IS NULL OR char_length(term) < 4 THEN
        RETURN NULL;
    END IF;
    SELECT array_agg(v.lexeme) INTO lexemes FROM unnest(to_tsvector('public.paperless_simple', term)) v;
    IF cardinality(lexemes) IS DISTINCT FROM 1 OR char_length(lexemes[1]) < 4 THEN
        RETURN NULL;
    END IF;
    SELECT array_agg(v.lexeme) INTO stems FROM unnest(to_tsvector('pg_catalog.german', term)) v;
    IF cardinality(stems) = 1 AND stems[1] <> lexemes[1] AND char_length(stems[1]) >= 4 THEN
        RETURN lexemes || stems[1];
    END IF;
    RETURN lexemes;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION document_search_tsquery(
    owner bigint, terms text[], is_phrase boolean[], negated boolean[], or_group integer[]
) RETURNS tsquery
LANGUAGE plpgsql STABLE AS $$
DECLARE
    n integer := coalesce(cardinality(terms), 0);
    item record;
    q tsquery;
    part tsquery;
    groups tsquery[] := '{}';
    group_ids integer[] := '{}';
    pos integer;
    pattern text;
    result tsquery;
    patterns text[];
    escaped text[];
    compounds text[];
    found jsonb := '[]';
    counts integer[] := '{}';
    alloc integer[];
    remaining integer := 250;
    sharing integer;
    k integer;
    i integer := 0;
BEGIN
    IF coalesce(cardinality(is_phrase), 0) <> n OR coalesce(cardinality(negated), 0) <> n
       OR coalesce(cardinality(or_group), 0) <> n THEN
        RAISE EXCEPTION 'document_search_tsquery: arrays of different lengths' USING ERRCODE = '22023';
    END IF;
    -- Compounds, first pass: each item's lookup, at most 100 lexemes in
    -- their final order, and how many it found. The loop below takes the
    -- first alloc[i] of item i.
    FOR item IN
        SELECT u.term, coalesce(u.phrase, false) AS phrase
        FROM unnest(terms, is_phrase) WITH ORDINALITY AS u(term, phrase, ord)
        WHERE u.term IS NOT NULL AND btrim(u.term) <> ''
        ORDER BY u.ord
    LOOP
        compounds := NULL;
        patterns := CASE WHEN item.phrase THEN NULL ELSE document_search_compound_patterns(item.term) END;
        IF patterns IS NOT NULL THEN
            SELECT array_agg(replace(replace(replace(p, '\', '\\'), '%', '\%'), '_', '\_'))
            INTO escaped FROM unnest(patterns) p;
            -- The values are bound, so the statement is planned with
            -- them and the trigram index serves the LIKE. The owner's
            -- most frequent first (all owners' counts summed for an
            -- admin), then by lexeme, so a word and its negation expand
            -- alike. TestSearchCompoundLookupUsesTrigramIndex reads this
            -- statement from the function and explains it.
            EXECUTE $lookup$
                SELECT array_agg(c.lexeme ORDER BY c.n DESC, c.lexeme) FROM (
                    SELECT v.lexeme, sum(v.n) AS n
                    FROM search_vocabulary v
                    WHERE ($1::bigint IS NULL OR v.owner_id = $1::bigint)
                      AND v.lexeme LIKE ANY ($2::text[])
                      AND v.lexeme NOT LIKE ALL ($3::text[])
                    GROUP BY v.lexeme
                    ORDER BY sum(v.n) DESC, v.lexeme
                    LIMIT 100
                ) c
            $lookup$
            INTO compounds
            USING owner,
                (SELECT array_agg('%' || e.p || '%') FROM unnest(escaped) e(p)),
                (SELECT array_agg(e.p || '%') FROM unnest(escaped) e(p));
        END IF;
        found := found || jsonb_build_array(coalesce(to_jsonb(compounds), '[]'::jsonb));
        counts := counts || coalesce(cardinality(compounds), 0);
    END LOOP;
    -- The 250 of the search, shared fairly: in ascending order of what
    -- they found (then by position), each item gets what it found, at
    -- most an equal share of what is left. A single word keeps its 100;
    -- two words 100 each; 32 words about 7 each when all find many.
    alloc := array_fill(0, ARRAY[greatest(cardinality(counts), 1)]);
    SELECT count(*) INTO sharing FROM unnest(counts) c WHERE c > 0;
    FOR k IN
        SELECT s.i FROM generate_subscripts(counts, 1) AS s(i) WHERE counts[s.i] > 0 ORDER BY counts[s.i], s.i
    LOOP
        alloc[k] := least(counts[k], remaining / sharing);
        remaining := remaining - alloc[k];
        sharing := sharing - 1;
    END LOOP;

    FOR item IN
        SELECT u.term, coalesce(u.phrase, false) AS phrase, coalesce(u.neg, false) AS neg, u.grp, u.ord
        FROM unnest(terms, is_phrase, negated, or_group) WITH ORDINALITY AS u(term, phrase, neg, grp, ord)
        WHERE u.term IS NOT NULL AND btrim(u.term) <> ''
        ORDER BY u.ord
    LOOP
        i := i + 1;
        IF item.phrase THEN
            q := phraseto_tsquery('pg_catalog.german', item.term)
              || phraseto_tsquery('pg_catalog.english', item.term)
              || phraseto_tsquery('public.paperless_simple', item.term);
        ELSE
            q := plainto_tsquery('pg_catalog.german', item.term)
              || plainto_tsquery('pg_catalog.english', item.term)
              || plainto_tsquery('public.paperless_simple', item.term);
            IF char_length(item.term) >= 3 THEN
                SELECT string_agg('''' || replace(replace(v.lexeme, '\', '\\'), '''', '''''') || ''':*', ' & ')::tsquery
                INTO part
                FROM unnest(to_tsvector('public.paperless_simple', item.term)) v;
                IF part IS NOT NULL THEN
                    q := q || part;
                END IF;
            END IF;
            -- Compounds: this item's share of the first pass.
            IF alloc[i] > 0 THEN
                SELECT string_agg('''' || replace(replace(e.l, '\', '\\'), '''', '''''') || '''', ' | ' ORDER BY e.ord)::tsquery
                INTO part
                FROM jsonb_array_elements_text(found -> (i - 1)) WITH ORDINALITY AS e(l, ord)
                WHERE e.ord <= alloc[i];
                q := q || part;
            END IF;
        END IF;

        -- Names: substring for 3 or more characters, else the whole name.
        -- LIKE wildcards in the term are escaped (the escape character is
        -- the default backslash).
        IF char_length(item.term) >= 3 THEN
            pattern := '%' || replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_') || '%';
        ELSE
            pattern := replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_');
        END IF;
        SELECT string_agg('''' || l.lexeme || '''', ' | ')::tsquery INTO part
        FROM (
            SELECT m.lexeme FROM (
                WITH RECURSIVE matched (id) AS (
                    SELECT t.id FROM tags t
                    WHERE (owner IS NULL OR t.owner_id = owner)
                      AND public.unaccent('public.unaccent', t.name) ILIKE pattern
                    UNION
                    SELECT c.id FROM tags c JOIN matched m ON c.parent_id = m.id
                )
                SELECT documents_search_link_lexeme('t', id) AS lexeme, 3 AS k, id FROM matched
                UNION ALL
                SELECT documents_search_link_lexeme('c', c.id), 1, c.id FROM correspondents c
                WHERE (owner IS NULL OR c.owner_id = owner)
                  AND public.unaccent('public.unaccent', c.name) ILIKE pattern
                UNION ALL
                SELECT documents_search_link_lexeme('d', y.id), 2, y.id FROM document_types y
                WHERE (owner IS NULL OR y.owner_id = owner)
                  AND public.unaccent('public.unaccent', y.name) ILIKE pattern
            ) m
            ORDER BY m.k, m.id
            LIMIT 1000
        ) l;
        IF part IS NOT NULL THEN
            q := q || part;
        END IF;

        IF numnode(q) = 0 THEN
            CONTINUE;
        END IF;
        IF item.neg THEN
            q := !! q;
        END IF;
        pos := array_position(group_ids, item.grp);
        IF item.grp IS NULL OR pos IS NULL THEN
            groups := groups || q;
            group_ids := group_ids || item.grp;
        ELSE
            groups[pos] := groups[pos] || q;
        END IF;
    END LOOP;
    FOREACH q IN ARRAY groups LOOP
        result := CASE WHEN result IS NULL THEN q ELSE result && q END;
    END LOOP;
    RETURN result;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION document_search_compound_patterns(text);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION document_search_tsquery(
    owner bigint, terms text[], is_phrase boolean[], negated boolean[], or_group integer[]
) RETURNS tsquery
LANGUAGE plpgsql STABLE AS $$
DECLARE
    n integer := coalesce(cardinality(terms), 0);
    item record;
    q tsquery;
    part tsquery;
    groups tsquery[] := '{}';
    group_ids integer[] := '{}';
    pos integer;
    pattern text;
    result tsquery;
BEGIN
    IF coalesce(cardinality(is_phrase), 0) <> n OR coalesce(cardinality(negated), 0) <> n
       OR coalesce(cardinality(or_group), 0) <> n THEN
        RAISE EXCEPTION 'document_search_tsquery: arrays of different lengths' USING ERRCODE = '22023';
    END IF;
    FOR item IN
        SELECT u.term, coalesce(u.phrase, false) AS phrase, coalesce(u.neg, false) AS neg, u.grp, u.ord
        FROM unnest(terms, is_phrase, negated, or_group) WITH ORDINALITY AS u(term, phrase, neg, grp, ord)
        WHERE u.term IS NOT NULL AND btrim(u.term) <> ''
        ORDER BY u.ord
    LOOP
        IF item.phrase THEN
            q := phraseto_tsquery('pg_catalog.german', item.term)
              || phraseto_tsquery('pg_catalog.english', item.term)
              || phraseto_tsquery('public.paperless_simple', item.term);
        ELSE
            q := plainto_tsquery('pg_catalog.german', item.term)
              || plainto_tsquery('pg_catalog.english', item.term)
              || plainto_tsquery('public.paperless_simple', item.term);
            IF char_length(item.term) >= 3 THEN
                SELECT string_agg('''' || replace(replace(v.lexeme, '\', '\\'), '''', '''''') || ''':*', ' & ')::tsquery
                INTO part
                FROM unnest(to_tsvector('public.paperless_simple', item.term)) v;
                IF part IS NOT NULL THEN
                    q := q || part;
                END IF;
            END IF;
        END IF;

        -- Names: substring for 3 or more characters, else the whole name.
        -- LIKE wildcards in the term are escaped (the escape character is
        -- the default backslash).
        IF char_length(item.term) >= 3 THEN
            pattern := '%' || replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_') || '%';
        ELSE
            pattern := replace(replace(replace(public.unaccent('public.unaccent', item.term),
                       '\', '\\'), '%', '\%'), '_', '\_');
        END IF;
        SELECT string_agg('''' || l.lexeme || '''', ' | ')::tsquery INTO part
        FROM (
            SELECT m.lexeme FROM (
                WITH RECURSIVE matched (id) AS (
                    SELECT t.id FROM tags t
                    WHERE (owner IS NULL OR t.owner_id = owner)
                      AND public.unaccent('public.unaccent', t.name) ILIKE pattern
                    UNION
                    SELECT c.id FROM tags c JOIN matched m ON c.parent_id = m.id
                )
                SELECT documents_search_link_lexeme('t', id) AS lexeme, 3 AS k, id FROM matched
                UNION ALL
                SELECT documents_search_link_lexeme('c', c.id), 1, c.id FROM correspondents c
                WHERE (owner IS NULL OR c.owner_id = owner)
                  AND public.unaccent('public.unaccent', c.name) ILIKE pattern
                UNION ALL
                SELECT documents_search_link_lexeme('d', y.id), 2, y.id FROM document_types y
                WHERE (owner IS NULL OR y.owner_id = owner)
                  AND public.unaccent('public.unaccent', y.name) ILIKE pattern
            ) m
            ORDER BY m.k, m.id
            LIMIT 1000
        ) l;
        IF part IS NOT NULL THEN
            q := q || part;
        END IF;

        IF numnode(q) = 0 THEN
            CONTINUE;
        END IF;
        IF item.neg THEN
            q := !! q;
        END IF;
        pos := array_position(group_ids, item.grp);
        IF item.grp IS NULL OR pos IS NULL THEN
            groups := groups || q;
            group_ids := group_ids || item.grp;
        ELSE
            groups[pos] := groups[pos] || q;
        END IF;
    END LOOP;
    FOREACH q IN ARRAY groups LOOP
        result := CASE WHEN result IS NULL THEN q ELSE result && q END;
    END LOOP;
    RETURN result;
END;
$$;
-- +goose StatementEnd
