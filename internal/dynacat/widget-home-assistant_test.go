package dynacat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestHomeAssistantSelectRow(t *testing.T) {
	available := []string{"Home", "Away", "Night", "Visitors"}

	tests := []struct {
		configured []string
		state      string
		want       []string
	}{
		{nil, "Home", available},
		{[]string{"Night", "Home", "Away"}, "Home", []string{"Night", "Home", "Away"}},
		{[]string{"Home", "Vacation"}, "Home", []string{"Home"}},
		{nil, "unavailable", nil},
	}

	widget := &homeAssistantWidget{}

	for _, test := range tests {
		cfg := &homeAssistantEntityConfig{Entity: "input_select.home_mode", Options: test.configured}
		state := &homeAssistantStateJSON{EntityID: cfg.Entity, State: test.state}
		state.Attributes.Options = available

		row := widget.buildRow(cfg, state)
		if !slices.Equal(row.Options, test.want) {
			t.Errorf("configured %v, state %q: options %v, want %v", test.configured, test.state, row.Options, test.want)
		}
		if test.want != nil && row.Selected != test.state {
			t.Errorf("selected %q, want %q", row.Selected, test.state)
		}
		if row.Toggleable {
			t.Error("select rows should not get a toggle")
		}
	}
}

func TestHomeAssistantSelectAction(t *testing.T) {
	var calledPath, calledBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calledPath, calledBody = r.URL.Path, string(body)
		w.Write([]byte(`[{"entity_id":"input_select.home_mode","state":"Night","attributes":{"options":["Home","Away","Night","Visitors"]}}]`)) //nolint:errcheck
	}))
	defer server.Close()

	widget := &homeAssistantWidget{URL: server.URL, Token: "token", Entities: []homeAssistantEntityConfig{{Entity: "input_select.home_mode", Options: []string{"Home", "Away", "Night"}}}}
	state := &homeAssistantStateJSON{EntityID: "input_select.home_mode", State: "Home"}
	state.Attributes.Options = []string{"Home", "Away", "Night", "Visitors"}
	widget.Rows = []homeAssistantEntity{widget.buildRow(&widget.Entities[0], state)}

	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/api/widgets/1/action/select", strings.NewReader(body))
		request.SetPathValue("action", "select")
		recorder := httptest.NewRecorder()
		widget.handleRequest(recorder, request)
		return recorder
	}

	recorder := post(`{"entity":"input_select.home_mode","option":"Night"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if calledPath != "/api/services/input_select/select_option" {
		t.Errorf("called %q", calledPath)
	}
	var sent map[string]string
	json.Unmarshal([]byte(calledBody), &sent) //nolint:errcheck
	if sent["entity_id"] != "input_select.home_mode" || sent["option"] != "Night" {
		t.Errorf("sent %v", sent)
	}
	var response struct{ Selected string }
	json.Unmarshal(recorder.Body.Bytes(), &response) //nolint:errcheck
	if response.Selected != "Night" {
		t.Errorf("selected %q, want Night", response.Selected)
	}

	calledPath = ""
	for _, body := range []string{
		`{"entity":"input_select.home_mode","option":"Visitors"}`,
		`{"entity":"input_select.other","option":"Night"}`,
	} {
		if recorder := post(body); recorder.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", body, recorder.Code)
		}
	}
	if calledPath != "" {
		t.Errorf("rejected selections still called %q", calledPath)
	}

	request := httptest.NewRequest("POST", "/api/widgets/1/action/toggle", strings.NewReader(`{"entity":"input_select.home_mode"}`))
	request.SetPathValue("action", "toggle")
	recorder = httptest.NewRecorder()
	widget.handleRequest(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("toggle on a select: status %d, want 403", recorder.Code)
	}
}
