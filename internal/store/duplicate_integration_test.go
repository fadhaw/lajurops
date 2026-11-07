package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Duplicating a project copies its daily and hourly tasks, environments,
// owners, internal dependencies and runbooks, starting fresh.
func TestDuplicateTask(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	key := "DU" + suffix[len(suffix)-5:]
	ws, err := st.CreateWorkspace(ctx, WorkspaceInput{Key: key, Name: "dup"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM workspaces WHERE key = $1`, key) })
	alice, _ := st.UpsertUser(ctx, "itest-du-"+suffix, "alice"+suffix, "", "Alice")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, alice.ID) })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	project, err := st.CreateTask(ctx, TaskInput{WorkspaceID: ws.ID, Title: "Launch", Type: "project"}, "")
	must(err)
	envs, err := st.SetEnvironments(ctx, project.ID, []EnvironmentInput{{Name: "UAT", Color: "amber"}, {Name: "Prod", Color: "red"}})
	must(err)
	day := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	next := day.AddDate(0, 0, 1)
	est := 4.0
	prep, err := st.CreateTask(ctx, TaskInput{ParentID: &project.ID, Title: "Prepare", Type: "daily", Status: "done",
		AssigneeIDs: []string{alice.ID}, StartAt: &day, EndAt: &next, EstimateHours: &est, EnvironmentID: &envs[0].ID}, "")
	must(err)
	deploy, err := st.CreateTask(ctx, TaskInput{ParentID: &project.ID, Title: "Deploy", Type: "daily", EnvironmentID: &envs[1].ID}, "")
	must(err)
	must(st.AddDependency(ctx, deploy.ID, prep.ID))
	start, end := day.Add(9*time.Hour), day.Add(11*time.Hour)
	release, err := st.CreateTask(ctx, TaskInput{ParentID: &deploy.ID, Title: "Release", Type: "hourly", StartAt: &start, EndAt: &end}, "")
	must(err)
	sec, err := st.AddRunbookSection(ctx, release.ID, "Steps")
	must(err)
	step, err := st.AddRunbookStep(ctx, sec.ID, RunbookStepInput{Title: ptr("Backup")})
	must(err)
	_, err = st.UpdateRunbookStep(ctx, step.ID, alice.ID, RunbookStepInput{Done: ptr(true)})
	must(err)

	copy, err := st.DuplicateTask(ctx, project.ID, alice.ID)
	must(err)
	if copy.ID == project.ID || copy.Title != "Launch (copy)" || copy.Type != "project" || copy.ParentID != nil {
		t.Fatalf("copy = %+v", copy)
	}
	kids, err := st.ListTasks(ctx, TaskFilter{ParentID: copy.ID})
	must(err)
	if len(kids) != 2 {
		t.Fatalf("copy has %d children, want 2", len(kids))
	}
	copyEnvs, err := st.ListEnvironments(ctx, copy.ID)
	must(err)
	if len(copyEnvs) != 2 || copyEnvs[0].ID == envs[0].ID {
		t.Fatalf("copied environments = %+v", copyEnvs)
	}
	byTitle := map[string]Task{}
	for _, k := range kids {
		byTitle[k.Title] = k
	}
	p, d := byTitle["Prepare"], byTitle["Deploy"]
	if p.Status != "todo" || len(p.AssigneeIDs) != 1 || p.AssigneeIDs[0] != alice.ID || p.EstimateHours == nil || *p.EstimateHours != 4 ||
		p.StartAt == nil || !p.StartAt.Equal(day) {
		t.Errorf("copied Prepare = %+v", p)
	}
	if p.EnvironmentID == nil || *p.EnvironmentID != copyEnvs[0].ID || d.EnvironmentID == nil || *d.EnvironmentID != copyEnvs[1].ID {
		t.Errorf("copies point at environments %v, %v; want the copied project's", p.EnvironmentID, d.EnvironmentID)
	}
	if len(d.BlockedBy) != 1 || d.BlockedBy[0] != p.ID {
		t.Errorf("copied Deploy waits for %v, want the copied Prepare %s", d.BlockedBy, p.ID)
	}
	hourly, err := st.ListTasks(ctx, TaskFilter{ParentID: d.ID})
	must(err)
	if len(hourly) != 1 || hourly[0].Title != "Release" || hourly[0].RunbookTotal != 1 || hourly[0].RunbookDone != 0 {
		t.Fatalf("copied hourly = %+v", hourly)
	}
	// The original is untouched.
	orig, err := st.ListTasks(ctx, TaskFilter{ParentID: project.ID})
	must(err)
	if len(orig) != 2 {
		t.Errorf("original now has %d children", len(orig))
	}

	// Duplicating a task inside a project keeps it there, with its environment.
	again, err := st.DuplicateTask(ctx, prep.ID, alice.ID)
	must(err)
	if again.ParentID == nil || *again.ParentID != project.ID || again.EnvironmentID == nil || *again.EnvironmentID != envs[0].ID {
		t.Errorf("duplicated daily = %+v", again)
	}
	if _, err := st.DuplicateTask(ctx, "00000000-0000-0000-0000-000000000000", ""); err != ErrNotFound {
		t.Errorf("missing task: %v", err)
	}
}
