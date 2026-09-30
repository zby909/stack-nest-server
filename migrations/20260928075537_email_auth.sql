-- Create "auth_users" table
CREATE TABLE "auth_users" (
  "id" character varying NOT NULL,
  "email" character varying NOT NULL,
  "role" character varying NOT NULL,
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "auth_users_email_key" to table: "auth_users"
CREATE UNIQUE INDEX "auth_users_email_key" ON "auth_users" ("email");
-- Create "auth_challenges" table
CREATE TABLE "auth_challenges" (
  "id" character varying NOT NULL,
  "code_hash" character varying NOT NULL,
  "attempts" bigint NOT NULL DEFAULT 0,
  "sent_at" timestamptz NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "user_id" character varying NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_challenges_auth_users_challenges" FOREIGN KEY ("user_id") REFERENCES "auth_users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "authchallenge_user_id" to table: "auth_challenges"
CREATE UNIQUE INDEX "authchallenge_user_id" ON "auth_challenges" ("user_id");
-- Create "auth_sessions" table
CREATE TABLE "auth_sessions" (
  "id" character varying NOT NULL,
  "created_at" timestamptz NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "user_id" character varying NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "auth_sessions_auth_users_sessions" FOREIGN KEY ("user_id") REFERENCES "auth_users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "authsession_expires_at" to table: "auth_sessions"
CREATE INDEX "authsession_expires_at" ON "auth_sessions" ("expires_at");
