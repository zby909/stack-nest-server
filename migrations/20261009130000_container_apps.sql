CREATE TABLE container_apps (
 id text PRIMARY KEY,
 owner_user_id text NOT NULL REFERENCES auth_users(id) ON DELETE RESTRICT,
 name text NOT NULL,
 slug text NOT NULL UNIQUE,
 sealed text NOT NULL,
 memory_mib integer NOT NULL CHECK (memory_mib BETWEEN 64 AND 512),
 active_sealed text NOT NULL DEFAULT '',
 image_id text NOT NULL DEFAULT '',
 container_id text NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 1,
 action text NOT NULL DEFAULT 'deploy',
 status text NOT NULL DEFAULT 'pending',
 error text NOT NULL DEFAULT '',
 actor_id text NOT NULL,
 actor_email text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (generation > 0),
 CHECK (action IN ('', 'deploy','start','stop','restart','delete')),
 CHECK (status IN ('pending','deploying','ready','stopped','error'))
);
CREATE INDEX container_apps_owner ON container_apps(owner_user_id);
