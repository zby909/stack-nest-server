-- Create "projects" table.
CREATE TABLE "projects" (
    "id" character varying NOT NULL,
    "name" character varying NOT NULL,
    "slug" character varying(63) NOT NULL,
    "description" character varying NOT NULL DEFAULT '',
    "status" character varying NOT NULL,
    "created_at" timestamptz NOT NULL,
    "updated_at" timestamptz NOT NULL,
    PRIMARY KEY ("id"),
    CONSTRAINT "projects_slug_key" UNIQUE ("slug")
);

-- Create index "project_updated_at" to table: "projects".
CREATE INDEX "project_updated_at" ON "projects" ("updated_at");

-- Create "deployments" table.
CREATE TABLE "deployments" (
    "id" character varying NOT NULL,
    "status" character varying NOT NULL,
    "file_name" character varying NOT NULL,
    "created_at" timestamptz NOT NULL,
    "finished_at" timestamptz NULL,
    "error" character varying NOT NULL DEFAULT '',
    "project_id" character varying NOT NULL,
    PRIMARY KEY ("id"),
    CONSTRAINT "deployments_projects_deployments"
        FOREIGN KEY ("project_id")
        REFERENCES "projects" ("id")
        ON UPDATE NO ACTION
        ON DELETE CASCADE
);

-- Create index "deployment_project_id_created_at" to table: "deployments".
CREATE INDEX "deployment_project_id_created_at"
    ON "deployments" ("project_id", "created_at");
