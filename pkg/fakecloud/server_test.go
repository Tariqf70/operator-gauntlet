package fakecloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newTest(t *testing.T) (*Server, *httptest.Server, *fakeClock) {
	t.Helper()
	s := New()
	clk := &fakeClock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	s.SetClock(clk.Now)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts, clk
}

func do(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestCreateReturnsPasswordOnceAndConflictsOnName(t *testing.T) {
	s, ts, _ := newTest(t)
	resp, body := do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "orders-shop", "engine": "postgres", "sizeGB": 20})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	id, pw := body["id"].(string), body["password"].(string)
	if id == "" || pw == "" {
		t.Fatalf("missing id/password: %v", body)
	}
	if !s.Verify(id, pw) {
		t.Fatal("password from create should verify")
	}
	// GET must not leak the password.
	_, got := do(t, "GET", ts.URL+"/v1/databases/"+id, nil)
	if _, leaked := got["password"]; leaked {
		t.Fatal("GET leaked the password")
	}
	resp, _ = do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "orders-shop", "engine": "postgres", "sizeGB": 20})
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate name: got %d, want 409", resp.StatusCode)
	}
	if st := s.Stats(); st.CreateCalls != 2 || st.CreateConflicts != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestLifecycleTransitions(t *testing.T) {
	s, ts, clk := newTest(t)
	_, body := do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "a", "engine": "mysql", "sizeGB": 10})
	id := body["id"].(string)
	if s.List()[0].Status != StatusCreating {
		t.Fatal("new database should be creating")
	}
	resp, _ := do(t, "PATCH", ts.URL+"/v1/databases/"+id, map[string]any{"sizeGB": 30})
	if resp.StatusCode != 409 {
		t.Fatalf("resize while creating: got %d, want 409", resp.StatusCode)
	}
	clk.Add(s.CreateDuration)
	if s.List()[0].Status != StatusAvailable {
		t.Fatal("should be available after CreateDuration")
	}
	resp, body = do(t, "PATCH", ts.URL+"/v1/databases/"+id, map[string]any{"sizeGB": 30})
	if resp.StatusCode != 200 || body["status"] != StatusResizing {
		t.Fatalf("resize: %d %v", resp.StatusCode, body)
	}
	clk.Add(s.ResizeDuration)
	resp, _ = do(t, "DELETE", ts.URL+"/v1/databases/"+id, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	// Name stays reserved while deleting.
	resp, _ = do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "a", "engine": "mysql", "sizeGB": 10})
	if resp.StatusCode != 409 {
		t.Fatalf("create during delete: got %d, want 409", resp.StatusCode)
	}
	clk.Add(s.DeleteDuration)
	if len(s.List()) != 0 || len(s.Deleted()) != 1 {
		t.Fatalf("after delete: live=%v deleted=%v", s.List(), s.Deleted())
	}
}

func TestListByName(t *testing.T) {
	_, ts, _ := newTest(t)
	do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "x", "engine": "postgres", "sizeGB": 10})
	_, body := do(t, "GET", ts.URL+"/v1/databases?name=x", nil)
	if items := body["items"].([]any); len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	_, body = do(t, "GET", ts.URL+"/v1/databases?name=nope", nil)
	if items := body["items"].([]any); len(items) != 0 {
		t.Fatalf("items = %v", items)
	}
}

func TestResetPasswordRotates(t *testing.T) {
	s, ts, _ := newTest(t)
	_, body := do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "r", "engine": "postgres", "sizeGB": 10})
	id, old := body["id"].(string), body["password"].(string)
	_, body = do(t, "POST", ts.URL+"/v1/databases/"+id+"/reset-password", nil)
	nw := body["password"].(string)
	if nw == old || !s.Verify(id, nw) || s.Verify(id, old) {
		t.Fatal("reset-password should rotate the password")
	}
}

func TestOnCreateRunsBeforeResponse(t *testing.T) {
	s, ts, _ := newTest(t)
	var seen []Database
	s.OnCreate = func(db Database) {
		// The database must already be stored when the hook fires.
		if len(s.List()) != 1 {
			t.Error("hook fired before the database was stored")
		}
		seen = append(seen, db)
	}
	do(t, "POST", ts.URL+"/v1/databases", map[string]any{"name": "h", "engine": "postgres", "sizeGB": 10})
	if len(seen) != 1 || seen[0].Name != "h" {
		t.Fatalf("hook saw %v", seen)
	}
}

func TestValidation(t *testing.T) {
	_, ts, _ := newTest(t)
	for _, bad := range []map[string]any{
		{"name": "UPPER", "engine": "postgres", "sizeGB": 10},
		{"name": "ok", "engine": "oracle", "sizeGB": 10},
		{"name": "ok", "engine": "postgres", "sizeGB": 5},
	} {
		if resp, _ := do(t, "POST", ts.URL+"/v1/databases", bad); resp.StatusCode != 400 {
			t.Errorf("%v: got %d, want 400", bad, resp.StatusCode)
		}
	}
}
