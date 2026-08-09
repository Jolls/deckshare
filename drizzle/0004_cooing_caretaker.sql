DROP INDEX "notes_deck_guid_idx";--> statement-breakpoint
ALTER TABLE "decks" ADD COLUMN "anki_id" bigint;--> statement-breakpoint
ALTER TABLE "notes" ADD COLUMN "owner_id" uuid;--> statement-breakpoint
UPDATE "notes" SET "owner_id" = "decks"."owner_id" FROM "decks" WHERE "decks"."id" = "notes"."deck_id";--> statement-breakpoint
ALTER TABLE "notes" ALTER COLUMN "owner_id" SET NOT NULL;--> statement-breakpoint
ALTER TABLE "review_log" ADD COLUMN "anki_id" bigint;--> statement-breakpoint
ALTER TABLE "notes" ADD CONSTRAINT "notes_owner_id_users_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."users"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "decks_owner_name_idx" ON "decks" USING btree ("owner_id","name");--> statement-breakpoint
CREATE UNIQUE INDEX "note_types_owner_name_idx" ON "note_types" USING btree ("owner_id","name");--> statement-breakpoint
CREATE UNIQUE INDEX "notes_owner_guid_idx" ON "notes" USING btree ("owner_id","guid");--> statement-breakpoint
CREATE INDEX "notes_deck_idx" ON "notes" USING btree ("deck_id");--> statement-breakpoint
CREATE UNIQUE INDEX "review_log_user_card_anki_id_idx" ON "review_log" USING btree ("user_id","card_id","anki_id");