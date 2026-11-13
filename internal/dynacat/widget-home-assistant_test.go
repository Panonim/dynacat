package dynacat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHomeAssistantLockRow(t *testing.T) {
	tests := []struct {
		state      string
		codeFormat string
		service    string
		on         bool
		confirm    string
	}{
		{"locked", "", "unlock", true, "Unlock Front Door?"},
		{"unlocked", "", "lock", false, ""},
		{"jammed", "", "", false, ""},
		{"locking", "", "", false, ""},
		{"open", "", "", false, ""},
		{"unavailable", "", "", false, ""},
		{"locked", "number", "", false, ""},
	}

	widget := &homeAssistantWidget{}
	cfg := &homeAssistantEntityConfig{Entity: "lock.front_door", Title: "Front Door"}

	for _, test := range tests {
		state := &homeAssistantStateJSON{EntityID: cfg.Entity, State: test.state}
		state.Attributes.CodeFormat = test.codeFormat

		row := widget.buildRow(cfg, state)
		if row.service != test.service || row.Toggleable != (test.service != "") {
			t.Errorf("state %q, code format %q: service %q toggleable %v, want service %q", test.state, test.codeFormat, row.service, row.Toggleable, test.service)
		}
		if test.service != "" && row.On != test.on {
			t.Errorf("state %q: on = %v, want %v", test.state, row.On, test.on)
		}
		if row.Confirm != test.confirm {
			t.Errorf("state %q: confirm = %q, want %q", test.state, row.Confirm, test.confirm)
		}
	}
}

func TestHomeAssistantToggleRowUnchanged(t *testing.T) {
	widget := &homeAssistantWidget{}
	cfg := &homeAssistantEntityConfig{Entity: "light.kitchen"}

	row := widget.buildRow(cfg, &homeAssistantStateJSON{EntityID: cfg.Entity, State: "on"})
	if row.service != "toggle" || !row.Toggleable || !row.On || row.Confirm != "" {
		t.Errorf("light on: got service %q toggleable %v on %v confirm %q", row.service, row.Toggleable, row.On, row.Confirm)
	}

	row = widget.buildRow(cfg, &homeAssistantStateJSON{EntityID: cfg.Entity, State: "unavailable"})
	if row.Toggleable {
		t.Error("unavailable light should not be toggleable")
	}
}

func TestHomeAssistantLockActionCallsService(t *testing.T) {
	var called []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.Method+" "+r.URL.Path)
		next := map[string]string{"/api/services/lock/unlock": "unlocked", "/api/services/lock/lock": "locked"}[r.URL.Path]
		fmt.Fprintf(w, `[{"entity_id":"lock.front_door","state":%q,"attributes":{}}]`, next)
	}))
	defer server.Close()

	widget := &homeAssistantWidget{URL: server.URL, Token: "token", Entities: []homeAssistantEntityConfig{{Entity: "lock.front_door", Title: "Front Door"}}}
	widget.Rows = []homeAssistantEntity{widget.buildRow(&widget.Entities[0], &homeAssistantStateJSON{EntityID: "lock.front_door", State: "locked"})}

	for _, want := range []struct{ path, state string }{{"/api/services/lock/unlock", "Unlocked"}, {"/api/services/lock/lock", "Locked"}} {
		request := httptest.NewRequest("POST", "/api/widgets/1/action/toggle", strings.NewReader(`{"entity":"lock.front_door"}`))
		request.SetPathValue("action", "toggle")
		recorder := httptest.NewRecorder()

		widget.handleRequest(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		if last := called[len(called)-1]; last != "POST "+want.path {
			t.Errorf("called %q, want POST %s", last, want.path)
		}
		var response struct{ State string }
		json.Unmarshal(recorder.Body.Bytes(), &response) //nolint:errcheck
		if response.State != want.state {
			t.Errorf("state %q, want %q", response.State, want.state)
		}
	}
}
