# Anki schema diagram

Visual companion to [anki-schema.md](anki-schema.md), which is the source of truth for
column-level detail and verification status. This is Anki's own schema, not Enshu's — see
[schema-diagram.md](schema-diagram.md) for ours. Regenerate by hand if `anki-schema.md`'s
shape changes.

- [Shared tables](#shared-tables) — `notes`, `cards`, `revlog`, `graves`, present unchanged
  across schema versions
- [Legacy collection (schema ≤11)](#legacy-collection-schema-11) — content and config as
  JSON blobs inside `col`
- [Modern collection (schema 18+)](#modern-collection-schema-18) — content and config as
  real tables

> [!warning]
> Same unverified status as `anki-schema.md`: the legacy diagram matches the one real export
> inspected so far (✅, see `apkg-format.md`); the modern diagram is reconstructed from memory
> and unchecked against a real schema-18 file (❓).

---

## Shared tables

The four tables that don't change shape between schema versions. `cards` is drawn with both
`did`/`odid` — the filtered-deck pair where one shadows the other, detailed in
`anki-schema.md`.

```mermaid
erDiagram
    NOTES ||--o{ CARDS : "generates (nid)"
    CARDS ||--o{ REVLOG : "answered as (cid)"

    NOTES {
        int id PK "epoch ms"
        text guid "stable across export/import"
        int mid "FK note type"
        int mod "epoch seconds"
        int usn
        text tags "space-surrounded"
        text flds "joined by 0x1f, ordered by field ord"
        text sfld "denormalised sort field"
        int csum "truncated SHA-1 of first field"
        int flags "unused"
        text data "unused"
    }

    CARDS {
        int id PK "epoch ms"
        int nid FK
        int did FK "current deck (filtered deck while dyn)"
        int ord "template ordinal"
        int mod "epoch seconds"
        int usn
        int type "0 new,1 learning,2 review,3 relearning"
        int queue "due discriminator, not type"
        int due "position | epoch s | days since col.crt"
        int ivl "days(+) or seconds(-)"
        int factor "SM-2 ease x1000"
        int reps
        int lapses
        int left "steps-remaining packing"
        int odue "real due, while filtered"
        int odid "home deck, while filtered"
        int flags "low 3 bits = colour"
        text data "JSON: FSRS s/d/dr, pos"
    }

    REVLOG {
        int id PK "epoch ms = review time"
        int cid FK
        int usn
        int ease "1-4, 0 = manual"
        int ivl "days(+) or seconds(-)"
        int lastIvl "same encoding"
        int factor "0 if n/a"
        int time "ms taken"
        int type "learn/review/relearn/cram/manual"
    }

    GRAVES {
        int usn
        int oid "id of deleted object"
        int type "0 card,1 note,2 deck"
    }
```

`GRAVES` has no drawn edges — it's a tombstone log keyed loosely on `oid` against whichever
table `type` names, not a real foreign key, and it exists for sync propagation we don't use
(CLAUDE.md §9).

---

## Legacy collection (schema ≤11)

Note types, decks, and deck options as JSON objects inside the single-row `col` table, keyed
by entity id. ✅ Matches the one verified export.

```mermaid
erDiagram
    COL ||--o{ NOTES : "models[mid] describes"
    COL ||--o{ CARDS : "decks[did] describes"

    COL {
        int id PK "always 1"
        int crt "creation, epoch seconds"
        int mod "epoch ms"
        int scm "schema-mod time, epoch ms"
        int ver "schema version (not trustworthy alone)"
        int dty "unused"
        int usn
        int ls
        text conf "JSON: collection-level config"
        text models "JSON: {noteTypeId: {name,type,tmpls[],flds[],css,req,...}}"
        text decks "JSON: {deckId: {name,conf,dyn,desc,...}}"
        text dconf "JSON: {deckConfigId: {new{},rev{},lapse{},...}}"
        text tags "JSON: {tagName: usn}, legacy registry"
    }
```

`models[id].tmpls[].ord` and `flds[].ord` are authoritative over JSON array position — the
array can disagree with `ord` (`anki-schema.md`). Deck hierarchy in this form uses `::` as
the path separator, unlike schema 18's `\x1f`.

---

## Modern collection (schema 18+)

Note types, decks, and deck options promoted to real tables; per-row settings become a
protobuf blob instead of a JSON object value. ❓ Entirely unverified against a real file —
reconstructed from the format's known shape, same caveat as `anki-schema.md`.

```mermaid
erDiagram
    NOTETYPES ||--o{ FIELDS : "ntid"
    NOTETYPES ||--o{ TEMPLATES : "ntid"
    NOTETYPES ||--o{ NOTES : "mid"
    DECKS ||--o{ CARDS : "did"
    DECK_CONFIG ||--o{ DECKS : "config id, via kind"

    NOTETYPES {
        int id PK
        text name
        int mtime_secs
        int usn
        blob config "protobuf: type,sortf,css,req,..."
    }

    FIELDS {
        int ntid PK,FK
        int ord PK
        text name
        blob config "protobuf: sticky,rtl,font,size,..."
    }

    TEMPLATES {
        int ntid PK,FK
        int ord PK
        text name
        int mtime_secs
        int usn
        blob config "protobuf: q_format,a_format,browser variants,target deck"
    }

    DECKS {
        int id PK
        text name "0x1f-separated hierarchy"
        int mtime_secs
        int usn
        blob common "protobuf: collapsed state, etc."
        blob kind "protobuf oneof: normal | filtered params"
    }

    DECK_CONFIG {
        int id PK
        text name
        int mtime_secs
        int usn
        blob config "protobuf: steps, timer, FSRS params + desired retention"
    }

    CONFIG {
        text key PK
        int usn
        blob val "replaces col.conf, one row per setting"
    }

    TAGS {
        text tag PK
        int usn
    }
```

`NOTES`/`CARDS` themselves are unchanged from [Shared tables](#shared-tables) — only what
describes them moves. `col.models`/`col.decks`/`col.dconf` columns still exist on a schema-18
`col` row but are emptied, not dropped, which is why table presence (not `col.ver`) is the
reliable way to tell the two schemas apart (`anki-schema.md`).

`COLLATE unicase` is declared on the name columns here (`notetypes.name`, `decks.name`,
`fields.name`, `templates.name`) — a real client-side detail, not a Mermaid artifact, and the
reason `ORDER BY name` is avoided in favour of ordering by id.
