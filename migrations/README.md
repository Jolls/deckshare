# Migrations

Goose migration files, committed and immutable once merged (CLAUDE.md §9). Fix forward with a
new migration; never edit an applied one.

Apply:

```
goose -dir migrations postgres "$DATABASE_URL" up
```

Create a new migration:

```
goose -dir migrations create <name> sql
```

Schema §5 (docs/schema.md) lands in [#50](https://github.com/Jolls/enshu/issues/50).
