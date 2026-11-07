package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Rescheduled tasks stay open; canceled ones stop blocking and leave
// progress and workload. Projects take neither status.
func TestRescheduledAndCanceled(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	key := "ST" + suffix[len(suffix)-5:]
	ws, err := st.CreateWorkspace(ctx, WorkspaceInput{Key: key, Name: "status"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM workspaces WHERE key = $1`, key) })
	alice, _ := st.UpsertUser(ctx, "itest-st-"+suffix, "alice"+suffix, "", "Alice")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, alice.ID) })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	setStatus := func(id, status string) (Task, error) {
		raw, _ := json.Marshal(status)
		return st.UpdateTask(ctx, id, map[string]json.RawMessage{"status": raw})
	}

	project, err := st.CreateTask(ctx, TaskInput{WorkspaceID: ws.ID, Title: "P", Type: "project"}, "")
	must(err)
	day := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	end := day.Add(2 * time.Hour)
	a, err := st.CreateTask(ctx, TaskInput{ParentID: &project.ID, Title: "a", Type: "hourly", AssigneeIDs: []string{alice.ID}, StartAt: &day, EndAt: &end}, "")
	must(err)
	b, err := st.CreateTask(ctx, TaskInput{ParentID: &project.ID, Title: "b", Type: "daily", Status: "rescheduled"}, "")
	must(err)
	must(st.AddDependency(ctx, b.ID, a.ID))
	if b.Status != "rescheduled" {
		t.Fatalf("created with status %q", b.Status)
	}
	if b, _ = st.GetTask(ctx, b.ID); b.OpenBlockers != 1 {
		t.Fatalf("open blockers = %d, want 1", b.OpenBlockers)
	}

	_, err = setStatus(a.ID, "canceled")
	must(err)
	if b, _ = st.GetTask(ctx, b.ID); b.OpenBlockers != 0 {
		t.Errorf("a canceled blocker still blocks: %d", b.OpenBlockers)
	}
	progress, err := st.ProjectProgressFor(ctx, ws.ID)
	must(err)
	if len(progress) != 1 || progress[0].Total != 1 || progress[0].Done != 0 {
		t.Errorf("progress = %+v, want 0 of 1 (canceled left out)", progress)
	}
	p, _ := st.GetTask(ctx, project.ID)
	if p.SubtaskCount != 1 {
		t.Errorf("project subtask count = %d, want 1", p.SubtaskCount)
	}
	rows, err := st.WorkloadReport(ctx, day.AddDate(0, 0, -1), day.AddDate(0, 0, 1), Scope{ws.ID})
	must(err)
	for _, r := range rows {
		if r.UserID == alice.ID && (r.TotalTasks != 0 || r.HourlyHours != 0) {
			t.Errorf("canceled work counted for alice: %+v", r)
		}
	}

	_, err = setStatus(b.ID, "done")
	must(err)
	if p, _ = st.GetTask(ctx, project.ID); p.Progress != 100 {
		t.Errorf("project progress = %d, want 100 with the only counted task done", p.Progress)
	}
	if _, err := setStatus(project.ID, "canceled"); err == nil {
		t.Error("a project was canceled")
	}
	if _, err := st.CreateTask(ctx, TaskInput{WorkspaceID: ws.ID, Title: "Q", Type: "project", Status: "rescheduled"}, ""); err == nil {
		t.Error("a project was created rescheduled")
	}
}
