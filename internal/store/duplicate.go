package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DuplicateTask copies a task and everything inside it (a project's daily
// and hourly tasks, a daily task's hourly tasks) next to the original, under
// the same parent. Copies get new numbers and start fresh: status To Do, no
// progress, actual hours or comments, runbook steps unticked. They keep the
// title (the top one gains " (copy)"), description, priority, owners, dates,
// estimates, category and environment. A copied project gets its own copy
// of its environments; dependencies between copied tasks are copied too,
// and runbook steps linked to a copied task follow the copy (steps linked
// to other tasks become plain checklist items). Returns the top copy.
func (s *Store) DuplicateTask(ctx context.Context, id, actorID string) (Task, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(ctx)

	// The task and its descendants, parents first.
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE tree(id, depth) AS (
			SELECT id, 0 FROM tasks WHERE id = $1
			UNION ALL
			SELECT c.id, tree.depth + 1 FROM tasks c JOIN tree ON c.parent_id = tree.id
		)
		SELECT t.id::text, t.workspace_id::text, t.parent_id::text, t.title, t.description, t.type,
		       t.project_kind, t.priority, t.start_at, t.end_at, t.estimate_hours::float8,
		       t.environment_id::text, t.project_category_id::text
		FROM tree JOIN tasks t ON t.id = tree.id
		ORDER BY tree.depth, t.position, t.number`, id)
	if err != nil {
		return Task{}, mapConstraintErr(err)
	}
	type src struct {
		id, workspace           string
		parent                  *string
		title, description, typ string
		kind                    *string
		priority                string
		start, end              *time.Time
		estimate                *float64
		environment, category   *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (src, error) {
		var x src
		err := r.Scan(&x.id, &x.workspace, &x.parent, &x.title, &x.description, &x.typ,
			&x.kind, &x.priority, &x.start, &x.end, &x.estimate, &x.environment, &x.category)
		return x, err
	})
	if err != nil {
		return Task{}, err
	}
	if len(list) == 0 {
		return Task{}, ErrNotFound
	}
	root := list[0]

	// A copied project gets its own environments; tasks inside it point at
	// the copies. Elsewhere (a daily task in a project) environments stay.
	envMap := map[string]string{}
	newID := map[string]string{} // original task id -> copy id
	for i, t := range list {
		parent := t.parent
		title := t.title
		if i == 0 {
			title += " (copy)"
		} else {
			parent = ptr(newID[*t.parent])
		}
		var number int
		if err := tx.QueryRow(ctx, `UPDATE workspaces SET task_seq = task_seq + 1 WHERE id = $1 RETURNING task_seq`, t.workspace).Scan(&number); err != nil {
			return Task{}, err
		}
		var copyID string
		err := tx.QueryRow(ctx, `
			INSERT INTO tasks (workspace_id, parent_id, number, title, description, type, status, priority,
			                   reporter_id, start_at, end_at, estimate_hours, progress, project_kind,
			                   project_category_id, position)
			VALUES ($1, $2, $3, $4, $5, $6, 'todo', $7, $8, $9, $10, $11, 0, $12, $13,
			        (SELECT coalesce(max(position), 0) + 1 FROM tasks WHERE workspace_id = $1 AND status = 'todo'))
			RETURNING id::text`,
			t.workspace, parent, number, title, t.description, t.typ, t.priority,
			nullString(actorID), t.start, t.end, t.estimate, t.kind, t.category).Scan(&copyID)
		if err != nil {
			return Task{}, mapConstraintErr(err)
		}
		newID[t.id] = copyID
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_assignees (task_id, user_id)
			SELECT $2, user_id FROM task_assignees WHERE task_id = $1 ORDER BY assigned_at`, t.id, copyID); err != nil {
			return Task{}, err
		}
		if t.typ == "project" {
			envRows, err := tx.Query(ctx, `
				INSERT INTO project_environments (project_id, name, color, position)
				SELECT $2, name, color, position FROM project_environments WHERE project_id = $1
				RETURNING id::text, (SELECT e.id::text FROM project_environments e WHERE e.project_id = $1 AND lower(e.name) = lower(project_environments.name))`,
				t.id, copyID)
			if err != nil {
				return Task{}, err
			}
			pairs, err := pgx.CollectRows(envRows, func(r pgx.CollectableRow) ([2]string, error) {
				var p [2]string
				return p, r.Scan(&p[0], &p[1])
			})
			if err != nil {
				return Task{}, err
			}
			for _, p := range pairs {
				envMap[p[1]] = p[0]
			}
		}
		env := t.environment
		if env != nil {
			if mapped, ok := envMap[*env]; ok {
				env = &mapped
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET environment_id = $2 WHERE id = $1`, copyID, *env); err != nil {
				return Task{}, mapConstraintErr(err)
			}
		}
	}

	// Dependencies between copied tasks.
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_dependencies (task_id, depends_on_id)
		SELECT m1.new::uuid, m2.new::uuid
		FROM task_dependencies d
		JOIN jsonb_each_text($1::jsonb) m1(old, new) ON m1.old = d.task_id::text
		JOIN jsonb_each_text($1::jsonb) m2(old, new) ON m2.old = d.depends_on_id::text
		ON CONFLICT DO NOTHING`, newID); err != nil {
		return Task{}, mapConstraintErr(err)
	}
	// Runbooks, unticked.
	if _, err := tx.Exec(ctx, `
		WITH m AS (SELECT old::uuid AS old, new::uuid AS new FROM jsonb_each_text($1::jsonb) AS x(old, new)),
		secs AS (
			SELECT s.id AS old_id, gen_random_uuid() AS new_id, m.new AS task_id, s.name, s.notes, s.position
			FROM runbook_sections s JOIN m ON m.old = s.task_id
		),
		ins AS (
			INSERT INTO runbook_sections (id, task_id, name, notes, position)
			SELECT new_id, task_id, name, notes, position FROM secs
		)
		INSERT INTO runbook_steps (section_id, title, start_at, duration_minutes, notes, position, task_id)
		SELECT secs.new_id, st.title, st.start_at, st.duration_minutes, st.notes, st.position, lm.new
		FROM runbook_steps st JOIN secs ON secs.old_id = st.section_id
		LEFT JOIN m lm ON lm.old = st.task_id`, newID); err != nil {
		return Task{}, mapConstraintErr(err)
	}

	// The copies change their project's derived status and progress.
	top := newID[root.id]
	if project, err := rootProject(ctx, tx, &top); err != nil {
		return Task{}, err
	} else if err := recomputeProjects(ctx, tx, project); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	t, err := s.GetTask(ctx, top)
	if err == nil {
		s.notifyCreated(ctx, actorID, t)
	}
	return t, err
}
