package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Tasks nobody owns show up as a last "Unassigned" workload row.
func TestWorkloadUnassigned(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	key, other := "WU"+suffix[len(suffix)-5:], "WV"+suffix[len(suffix)-5:]
	ws, err := st.CreateWorkspace(ctx, WorkspaceInput{Key: key, Name: "workload"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws2, _ := st.CreateWorkspace(ctx, WorkspaceInput{Key: other, Name: "empty"}, "")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM workspaces WHERE key = ANY($1)`, []string{key, other}) })
	alice, _ := st.UpsertUser(ctx, "itest-wu-"+suffix, "alice"+suffix, "", "Alice")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, alice.ID) })

	day := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) *time.Time {
		v := day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		return &v
	}
	mk := func(title, typ string, owners []string, start, end *time.Time) {
		t.Helper()
		if _, err := st.CreateTask(ctx, TaskInput{WorkspaceID: ws.ID, Title: title, Type: typ, AssigneeIDs: owners, StartAt: start, EndAt: end}, ""); err != nil {
			t.Fatal(err)
		}
	}
	mk("alice 1", "hourly", []string{alice.ID}, at(9, 0), at(11, 0))
	mk("alice 2", "hourly", []string{alice.ID}, at(10, 0), at(12, 0))
	// Unassigned: two overlapping hourly tasks (13:00-15:00 counted once) and a daily one.
	mk("deploy", "hourly", nil, at(13, 0), at(14, 0))
	mk("smoke test", "hourly", nil, at(13, 30), at(15, 0))
	next := day.AddDate(0, 0, 1)
	mk("prepare", "daily", nil, &day, &next)
	later := day.AddDate(0, 0, 10)
	laterEnd := later.Add(time.Hour)
	mk("next week", "hourly", nil, &later, &laterEnd) // outside the period

	from, to := day.AddDate(0, 0, -1), day.AddDate(0, 0, 6)
	rows, err := st.WorkloadReport(ctx, from, to, Scope{ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	last := rows[len(rows)-1]
	if !last.Unassigned || last.UserID != UnassignedID || last.DisplayName != "Unassigned" {
		t.Fatalf("last row = %+v", last)
	}
	if last.HourlyTasks != 2 || last.HourlyHours != 2 || last.DailyTasks != 1 || last.TotalTasks != 3 {
		t.Errorf("unassigned row = %+v, want 2 hourly (2h, overlap once), 1 daily", last)
	}
	for _, r := range rows {
		if r.UserID == alice.ID && (r.HourlyHours != 3 || r.HourlyTasks != 2) {
			t.Errorf("alice = %+v, want 2 hourly tasks, 3h", r)
		}
	}
	tasks, err := st.WorkloadTasks(ctx, UnassignedID, from, to, Scope{ws.ID})
	if err != nil || len(tasks) != 3 {
		t.Fatalf("unassigned tasks = %d, %v", len(tasks), err)
	}
	for _, x := range tasks {
		if len(x.AssigneeIDs) != 0 {
			t.Errorf("%s has owners", x.Title)
		}
	}

	// No unassigned work: no row.
	rows, _ = st.WorkloadReport(ctx, from, to, Scope{ws2.ID})
	for _, r := range rows {
		if r.Unassigned {
			t.Errorf("unassigned row with nothing unassigned: %+v", r)
		}
	}
}

// Workload and commitments scoped to a workspace list only its members
// (plus anyone with tasks in it), and a user's default workspace round-trips.
func TestReportsListOnlyMembers(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	k1, k2 := "RA"+suffix[len(suffix)-5:], "RB"+suffix[len(suffix)-5:]
	ws1, err := st.CreateWorkspace(ctx, WorkspaceInput{Key: k1, Name: "one"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws2, _ := st.CreateWorkspace(ctx, WorkspaceInput{Key: k2, Name: "two"}, "")
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM workspaces WHERE key = ANY($1)`, []string{k1, k2}) })
	user := func(n string) User {
		u, err := st.UpsertUser(ctx, "itest-rm-"+n+suffix, n+suffix, "", n)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID) })
		return u
	}
	one, twoA, twoB := user("one"), user("twoa"), user("twob")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetUserAccess(ctx, one.ID, map[string]string{ws1.ID: RoleEditor, ws2.ID: RoleNone}))
	must(st.SetUserAccess(ctx, twoA.ID, map[string]string{ws1.ID: RoleNone, ws2.ID: RoleEditor}))
	must(st.SetUserAccess(ctx, twoB.ID, map[string]string{ws1.ID: RoleNone, ws2.ID: RoleViewer}))

	week := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	names := func(scope Scope) (workload, commitments map[string]bool) {
		t.Helper()
		workload, commitments = map[string]bool{}, map[string]bool{}
		rows, err := st.WorkloadReport(ctx, week, week.AddDate(0, 0, 7), scope)
		must(err)
		for _, r := range rows {
			workload[r.UserID] = true
		}
		list, err := st.Commitments(ctx, week, scope, "", true)
		must(err)
		for _, c := range list {
			commitments[c.UserID] = true
		}
		return
	}
	w, c := names(Scope{ws2.ID})
	for _, m := range []map[string]bool{w, c} {
		if m[one.ID] || !m[twoA.ID] || !m[twoB.ID] {
			t.Errorf("workspace two lists one=%v twoA=%v twoB=%v, want only the two members", m[one.ID], m[twoA.ID], m[twoB.ID])
		}
	}
	// Work assigned in a workspace lists its owner there, member or not.
	start := week.Add(9 * time.Hour)
	end := start.Add(time.Hour)
	_, err = st.CreateTask(ctx, TaskInput{WorkspaceID: ws2.ID, Title: "help out", Type: "hourly", AssigneeIDs: []string{one.ID}, StartAt: &start, EndAt: &end}, "")
	must(err)
	if w, c := names(Scope{ws2.ID}); !w[one.ID] || !c[one.ID] {
		t.Errorf("owner of a task in workspace two not listed: workload=%v commitments=%v", w[one.ID], c[one.ID])
	}
	// Every workspace (admins): everyone.
	if w, c := names(nil); !w[one.ID] || !w[twoA.ID] || !c[twoB.ID] {
		t.Error("unscoped report misses users")
	}

	must(st.SetDefaultWorkspace(ctx, twoA.ID, ws2.ID))
	if ws, err := st.DefaultWorkspace(ctx, twoA.ID); err != nil || ws != ws2.ID {
		t.Errorf("default workspace = %q, %v", ws, err)
	}
	must(st.SetDefaultWorkspace(ctx, twoA.ID, ""))
	if ws, _ := st.DefaultWorkspace(ctx, twoA.ID); ws != "" {
		t.Errorf("cleared default workspace = %q", ws)
	}
	if err := st.SetDefaultWorkspace(ctx, twoA.ID, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("unknown workspace accepted")
	}
}
