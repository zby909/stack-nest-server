-- Associate every project with a login user; the admin owns pre-existing projects.
ALTER TABLE "projects" ADD COLUMN "owner_user_id" character varying NULL;

DO $$
DECLARE admin_id character varying;
BEGIN
  IF EXISTS (SELECT 1 FROM projects WHERE owner_user_id IS NULL) THEN
    SELECT id INTO admin_id FROM auth_users WHERE role = 'admin' ORDER BY created_at, id LIMIT 1;
    IF admin_id IS NULL THEN
      RAISE EXCEPTION 'cannot assign existing projects: no administrator exists';
    END IF;
    UPDATE projects SET owner_user_id = admin_id WHERE owner_user_id IS NULL;
  END IF;
END $$;

ALTER TABLE "projects" ALTER COLUMN "owner_user_id" SET NOT NULL;
ALTER TABLE "projects" ADD CONSTRAINT "projects_owner_user_id_fkey"
  FOREIGN KEY ("owner_user_id") REFERENCES "auth_users" ("id") ON DELETE RESTRICT;
CREATE INDEX "project_owner_user_id" ON "projects" ("owner_user_id");
