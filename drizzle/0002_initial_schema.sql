CREATE TYPE "public"."deck_role" AS ENUM('owner', 'editor', 'viewer');--> statement-breakpoint
CREATE TYPE "public"."deck_visibility" AS ENUM('private', 'public');--> statement-breakpoint
CREATE TABLE "cards" (
	"id" uuid PRIMARY KEY NOT NULL,
	"note_id" uuid NOT NULL,
	"template_id" uuid NOT NULL,
	"ordinal" smallint NOT NULL,
	"deck_id" uuid NOT NULL,
	"anki_id" bigint
);
--> statement-breakpoint
CREATE TABLE "deck_access" (
	"deck_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"role" "deck_role" NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "deck_access_deck_id_user_id_pk" PRIMARY KEY("deck_id","user_id")
);
--> statement-breakpoint
CREATE TABLE "decks" (
	"id" uuid PRIMARY KEY NOT NULL,
	"owner_id" uuid NOT NULL,
	"name" text NOT NULL,
	"description" text DEFAULT '' NOT NULL,
	"visibility" "deck_visibility" DEFAULT 'private' NOT NULL,
	"forked_from_deck_id" uuid,
	"preset" jsonb,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"modified_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "fields" (
	"id" uuid PRIMARY KEY NOT NULL,
	"note_type_id" uuid NOT NULL,
	"ordinal" smallint NOT NULL,
	"name" text NOT NULL,
	"font" text DEFAULT 'Arial' NOT NULL,
	"size" smallint DEFAULT 20 NOT NULL,
	"is_rtl" boolean DEFAULT false NOT NULL,
	"sticky" boolean DEFAULT false NOT NULL
);
--> statement-breakpoint
CREATE TABLE "media_blobs" (
	"sha256" text PRIMARY KEY NOT NULL,
	"size_bytes" integer NOT NULL,
	"mime" text NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "media_refs" (
	"deck_id" uuid NOT NULL,
	"filename" text NOT NULL,
	"sha256" text NOT NULL,
	CONSTRAINT "media_refs_deck_id_filename_pk" PRIMARY KEY("deck_id","filename")
);
--> statement-breakpoint
CREATE TABLE "note_types" (
	"id" uuid PRIMARY KEY NOT NULL,
	"owner_id" uuid NOT NULL,
	"name" text NOT NULL,
	"css" text DEFAULT '' NOT NULL,
	"is_cloze" boolean DEFAULT false NOT NULL,
	"sort_field_idx" smallint DEFAULT 0 NOT NULL,
	"anki_id" bigint
);
--> statement-breakpoint
CREATE TABLE "notes" (
	"id" uuid PRIMARY KEY NOT NULL,
	"guid" text NOT NULL,
	"note_type_id" uuid NOT NULL,
	"deck_id" uuid NOT NULL,
	"fields" jsonb NOT NULL,
	"tags" text[] DEFAULT '{}'::text[] NOT NULL,
	"checksum" bigint,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"modified_at" timestamp with time zone DEFAULT now() NOT NULL,
	"anki_id" bigint
);
--> statement-breakpoint
CREATE TABLE "review_log" (
	"id" uuid PRIMARY KEY NOT NULL,
	"user_id" uuid NOT NULL,
	"card_id" uuid NOT NULL,
	"rating" smallint NOT NULL,
	"reviewed_at" timestamp with time zone NOT NULL,
	"duration_ms" integer,
	"state_before" smallint NOT NULL,
	"learning_steps_before" smallint DEFAULT 0 NOT NULL,
	"stability_before" double precision,
	"difficulty_before" double precision,
	"elapsed_days_before" integer NOT NULL,
	"scheduled_days_after" integer NOT NULL,
	"fsrs_version" smallint,
	"review_kind" smallint NOT NULL,
	CONSTRAINT "review_log_rating_range" CHECK ("review_log"."rating" between 1 and 4),
	CONSTRAINT "review_log_state_before_range" CHECK ("review_log"."state_before" between 0 and 3),
	CONSTRAINT "review_log_review_kind_range" CHECK ("review_log"."review_kind" between 0 and 4)
);
--> statement-breakpoint
CREATE TABLE "templates" (
	"id" uuid PRIMARY KEY NOT NULL,
	"note_type_id" uuid NOT NULL,
	"ordinal" smallint NOT NULL,
	"name" text NOT NULL,
	"qfmt" text NOT NULL,
	"afmt" text NOT NULL,
	"browser_qfmt" text,
	"browser_afmt" text
);
--> statement-breakpoint
CREATE TABLE "user_card_state" (
	"user_id" uuid NOT NULL,
	"card_id" uuid NOT NULL,
	"due" timestamp with time zone NOT NULL,
	"stability" double precision DEFAULT 0 NOT NULL,
	"difficulty" double precision DEFAULT 0 NOT NULL,
	"state" smallint DEFAULT 0 NOT NULL,
	"reps" integer DEFAULT 0 NOT NULL,
	"lapses" integer DEFAULT 0 NOT NULL,
	"elapsed_days" integer DEFAULT 0 NOT NULL,
	"scheduled_days" integer DEFAULT 0 NOT NULL,
	"learning_steps" smallint DEFAULT 0 NOT NULL,
	"last_review" timestamp with time zone,
	"suspended" boolean DEFAULT false NOT NULL,
	"buried_until" date,
	"flag" smallint DEFAULT 0 NOT NULL,
	CONSTRAINT "user_card_state_user_id_card_id_pk" PRIMARY KEY("user_id","card_id"),
	CONSTRAINT "user_card_state_state_range" CHECK ("user_card_state"."state" between 0 and 3)
);
--> statement-breakpoint
CREATE TABLE "user_fsrs_params" (
	"id" uuid PRIMARY KEY NOT NULL,
	"user_id" uuid NOT NULL,
	"deck_id" uuid,
	"fsrs_version" smallint NOT NULL,
	"params" jsonb NOT NULL,
	"desired_retention" double precision DEFAULT 0.9 NOT NULL,
	"optimised_at" timestamp with time zone,
	"review_count_at_fit" integer,
	CONSTRAINT "user_fsrs_params_user_deck_key" UNIQUE NULLS NOT DISTINCT("user_id","deck_id"),
	CONSTRAINT "user_fsrs_params_desired_retention_range" CHECK ("user_fsrs_params"."desired_retention" > 0 and "user_fsrs_params"."desired_retention" < 1)
);
--> statement-breakpoint
CREATE TABLE "users" (
	"id" uuid PRIMARY KEY NOT NULL,
	"email" text NOT NULL,
	"display_name" text NOT NULL,
	"timezone" text DEFAULT 'UTC' NOT NULL,
	"day_start_hour" smallint DEFAULT 4 NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "users_day_start_hour_range" CHECK ("users"."day_start_hour" between 0 and 23)
);
--> statement-breakpoint
ALTER TABLE "cards" ADD CONSTRAINT "cards_note_id_notes_id_fk" FOREIGN KEY ("note_id") REFERENCES "public"."notes"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cards" ADD CONSTRAINT "cards_template_id_templates_id_fk" FOREIGN KEY ("template_id") REFERENCES "public"."templates"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cards" ADD CONSTRAINT "cards_deck_id_decks_id_fk" FOREIGN KEY ("deck_id") REFERENCES "public"."decks"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "deck_access" ADD CONSTRAINT "deck_access_deck_id_decks_id_fk" FOREIGN KEY ("deck_id") REFERENCES "public"."decks"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "deck_access" ADD CONSTRAINT "deck_access_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "decks" ADD CONSTRAINT "decks_owner_id_users_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."users"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "decks" ADD CONSTRAINT "decks_forked_from_deck_id_decks_id_fk" FOREIGN KEY ("forked_from_deck_id") REFERENCES "public"."decks"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "fields" ADD CONSTRAINT "fields_note_type_id_note_types_id_fk" FOREIGN KEY ("note_type_id") REFERENCES "public"."note_types"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "media_refs" ADD CONSTRAINT "media_refs_deck_id_decks_id_fk" FOREIGN KEY ("deck_id") REFERENCES "public"."decks"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "media_refs" ADD CONSTRAINT "media_refs_sha256_media_blobs_sha256_fk" FOREIGN KEY ("sha256") REFERENCES "public"."media_blobs"("sha256") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "note_types" ADD CONSTRAINT "note_types_owner_id_users_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."users"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "notes" ADD CONSTRAINT "notes_note_type_id_note_types_id_fk" FOREIGN KEY ("note_type_id") REFERENCES "public"."note_types"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "notes" ADD CONSTRAINT "notes_deck_id_decks_id_fk" FOREIGN KEY ("deck_id") REFERENCES "public"."decks"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "review_log" ADD CONSTRAINT "review_log_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "review_log" ADD CONSTRAINT "review_log_card_id_cards_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."cards"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "templates" ADD CONSTRAINT "templates_note_type_id_note_types_id_fk" FOREIGN KEY ("note_type_id") REFERENCES "public"."note_types"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "user_card_state" ADD CONSTRAINT "user_card_state_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "user_card_state" ADD CONSTRAINT "user_card_state_card_id_cards_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."cards"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "user_fsrs_params" ADD CONSTRAINT "user_fsrs_params_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "user_fsrs_params" ADD CONSTRAINT "user_fsrs_params_deck_id_decks_id_fk" FOREIGN KEY ("deck_id") REFERENCES "public"."decks"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "cards_note_ordinal_idx" ON "cards" USING btree ("note_id","ordinal");--> statement-breakpoint
CREATE INDEX "cards_deck_idx" ON "cards" USING btree ("deck_id");--> statement-breakpoint
CREATE INDEX "cards_template_idx" ON "cards" USING btree ("template_id");--> statement-breakpoint
CREATE INDEX "deck_access_user_idx" ON "deck_access" USING btree ("user_id");--> statement-breakpoint
CREATE UNIQUE INDEX "fields_note_type_ordinal_idx" ON "fields" USING btree ("note_type_id","ordinal");--> statement-breakpoint
CREATE INDEX "media_refs_sha256_idx" ON "media_refs" USING btree ("sha256");--> statement-breakpoint
CREATE UNIQUE INDEX "notes_deck_guid_idx" ON "notes" USING btree ("deck_id","guid");--> statement-breakpoint
CREATE INDEX "notes_note_type_idx" ON "notes" USING btree ("note_type_id");--> statement-breakpoint
CREATE INDEX "review_log_user_reviewed_idx" ON "review_log" USING btree ("user_id","reviewed_at");--> statement-breakpoint
CREATE INDEX "review_log_card_user_reviewed_idx" ON "review_log" USING btree ("card_id","user_id","reviewed_at");--> statement-breakpoint
CREATE UNIQUE INDEX "templates_note_type_ordinal_idx" ON "templates" USING btree ("note_type_id","ordinal");--> statement-breakpoint
CREATE INDEX "user_card_state_due_idx" ON "user_card_state" USING btree ("user_id","due") WHERE NOT "user_card_state"."suspended";--> statement-breakpoint
CREATE INDEX "user_card_state_card_idx" ON "user_card_state" USING btree ("card_id");--> statement-breakpoint
CREATE UNIQUE INDEX "users_email_lower_idx" ON "users" USING btree (lower("email"));