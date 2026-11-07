-- Two more statuses for daily and hourly tasks: rescheduled (still open,
-- moved to another time) and canceled (closed without being done; left
-- out of progress, workload and commitments). Projects derive their status
-- from their tasks and never take either.
ALTER TABLE tasks DROP CONSTRAINT tasks_status_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_status_check
    CHECK (status IN ('todo', 'in_progress', 'in_review', 'rescheduled', 'done', 'canceled'));
ALTER TABLE tasks ADD CONSTRAINT tasks_project_status
    CHECK (type <> 'project' OR status IN ('todo', 'in_progress', 'in_review', 'done'));
