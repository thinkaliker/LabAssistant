package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinkaliker/labassistant/manager/scheduler"
)

func newTaskDeps(t *testing.T) Deps {
	t.Helper()
	s, err := scheduler.Load(filepath.Join(t.TempDir(), "tasks.json"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("load scheduler: %v", err)
	}
	return Deps{Scheduler: s}
}

func putTask(t *testing.T, d Deps, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/v1/tasks/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	d.updateTask(w, r)
	return w
}

// The dashboard's task modal has no params or timezone editor, so its save omits both. They
// must survive: dropping params turned a task scoped to one compose stack into a whole-host
// run that recreated every container, and dropping timezone moved the fire time.
func TestUpdateTaskPreservesOmittedFields(t *testing.T) {
	d := newTaskDeps(t)
	created, err := d.Scheduler.Create(scheduler.Task{
		Name: "media images", Schedule: "0 3 * * *", Timezone: "Europe/London",
		Module: "duo", Action: "update", HostIDs: []string{"h1"},
		Params: json.RawMessage(`{"stack":"media"}`), AllowDestructive: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w := putTask(t, d, created.ID, `{"name":"media images","schedule":"0 4 * * *",
		"module":"duo","action":"update","hostIds":["h1"],"misfire":"skip",
		"interHostDelaySeconds":0,"enabled":true,"allowDestructive":true}`)
	if w.Code != 200 {
		t.Fatalf("got status %d, want 200: %s", w.Code, w.Body.String())
	}

	got, ok := d.Scheduler.Get(created.ID)
	if !ok {
		t.Fatal("task disappeared")
	}
	if string(got.Params) != `{"stack":"media"}` {
		t.Errorf("params = %q, want the stored value to survive an edit that omitted them", got.Params)
	}
	if got.Timezone != "Europe/London" {
		t.Errorf("timezone = %q, want it to survive an edit that omitted it", got.Timezone)
	}
	if got.Schedule != "0 4 * * *" {
		t.Errorf("schedule = %q, want the submitted value", got.Schedule)
	}
}

// Fields the client does send must still be applied, including ones set back to a zero value.
func TestUpdateTaskAppliesSubmittedFields(t *testing.T) {
	d := newTaskDeps(t)
	created, err := d.Scheduler.Create(scheduler.Task{
		Name: "nightly", Schedule: "0 3 * * *", Timezone: "Europe/London",
		Module: "qup", Action: "apply", HostIDs: []string{"h1", "h2"},
		Params: json.RawMessage(`{"stack":"media"}`), AllowDestructive: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w := putTask(t, d, created.ID, `{"name":"nightly","schedule":"0 3 * * *","timezone":"UTC",
		"module":"qup","action":"apply","hostIds":["h2"],"params":{"stack":"web"},
		"misfire":"catchup","enabled":false,"allowDestructive":true}`)
	if w.Code != 200 {
		t.Fatalf("got status %d, want 200: %s", w.Code, w.Body.String())
	}

	got, _ := d.Scheduler.Get(created.ID)
	if got.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", got.Timezone)
	}
	if string(got.Params) != `{"stack":"web"}` {
		t.Errorf("params = %q, want the submitted value", got.Params)
	}
	if len(got.HostIDs) != 1 || got.HostIDs[0] != "h2" {
		t.Errorf("hostIds = %v, want the submitted list to replace the stored one", got.HostIDs)
	}
	if got.Enabled {
		t.Error("enabled = true, want an explicit false to be applied")
	}
	if got.Misfire != "catchup" {
		t.Errorf("misfire = %q, want catchup", got.Misfire)
	}
}

func TestUpdateTaskUnknownIDIsNotFound(t *testing.T) {
	d := newTaskDeps(t)
	if w := putTask(t, d, "nope", `{"name":"x"}`); w.Code != 404 {
		t.Fatalf("got status %d, want 404", w.Code)
	}
}
