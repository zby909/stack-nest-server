-- Deployment notes also belong to unpublished preview versions.
ALTER TABLE "deployments" ADD COLUMN "notes" character varying NOT NULL DEFAULT '';
