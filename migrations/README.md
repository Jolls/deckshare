# Migrations

Goose migration files, committed and immutable once merged (CLAUDE.md §9). Fix forward with a
new migration; never edit an applied one.

Apply:

```
goose -dir migrations postgres "$DATABASE_URL" up
```

Create a new migration:

```
goose -dir migrations create -s <name> sql
```

The `-s` flag forces sequential numbering (`00015_foo.sql`) instead of goose's default
timestamp form. Several migrations authored in one sitting get near-identical timestamps that
sort by accident rather than by intent, and `sqlc` reads this directory in filename order, so
sequential numbering is the convention here, not the default.

`sqlc` reads these files directly as its schema source (`sqlc.yaml`'s `schema: migrations`) —
there is no separate Go-side schema definition. A migration that doesn't parse breaks
`sqlc generate`, not just `goose up`.

Schema §5 (docs/schema.md) landed in [#50](https://github.com/Jolls/deckshare/issues/50) as 14
migrations, one per table.

`00015` (#54) adds the `notes.owner_id = decks.owner_id` composite foreign key.
