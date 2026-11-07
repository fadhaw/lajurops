-- The workspace a user's Hourly Workload and Weekly Commitment views open
-- on (set by an admin); NULL shows every workspace they can see.
ALTER TABLE users ADD COLUMN default_workspace_id uuid REFERENCES workspaces(id) ON DELETE SET NULL;
