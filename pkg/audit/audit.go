// Package audit reads Kubernetes API server audit logs (JSON lines, audit.k8s.io/v1)
// and answers the questions the rules ask: how many writes did the manager make,
// did it write status through the subresource, and was it ever forbidden.
package audit

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"time"
)

// Event is the subset of audit.k8s.io/v1 Event the suite needs.
type Event struct {
	Stage      string `json:"stage"`
	Verb       string `json:"verb"`
	RequestURI string `json:"requestURI"`
	User       struct {
		Username string   `json:"username"`
		Groups   []string `json:"groups"`
	} `json:"user"`
	ObjectRef *struct {
		Resource    string `json:"resource"`
		Subresource string `json:"subresource"`
		Namespace   string `json:"namespace"`
		Name        string `json:"name"`
		APIGroup    string `json:"apiGroup"`
	} `json:"objectRef"`
	ResponseStatus *struct {
		Code int `json:"code"`
	} `json:"responseStatus"`
	RequestReceivedTimestamp time.Time `json:"requestReceivedTimestamp"`
	StageTimestamp           time.Time `json:"stageTimestamp"`
}

// WriteVerbs are the verbs that mutate state.
var WriteVerbs = map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "deletecollection": true}

// IsWrite reports whether the event mutates state.
func (e Event) IsWrite() bool { return WriteVerbs[e.Verb] }

// Code returns the response code, or 0 if unknown.
func (e Event) Code() int {
	if e.ResponseStatus == nil {
		return 0
	}
	return e.ResponseStatus.Code
}

// Read parses JSON lines. Lines that fail to parse (for example a partially
// written last line) are skipped and counted.
func Read(r io.Reader) (events []Event, skipped int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	return events, skipped, sc.Err()
}

// ReadFile parses an audit log file.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ev, _, err := Read(f)
	return ev, err
}

// Filter selects events.
type Filter struct {
	User     string    // exact username; empty matches all
	Since    time.Time // inclusive, by RequestReceivedTimestamp; zero = no bound
	Until    time.Time // exclusive; zero = no bound
	Resource string    // objectRef.resource; empty matches all
	APIGroup *string   // objectRef.apiGroup; nil matches all
}

func (f Filter) match(e Event) bool {
	// Count each request once, at completion.
	if e.Stage != "" && e.Stage != "ResponseComplete" {
		return false
	}
	if f.User != "" && e.User.Username != f.User {
		return false
	}
	t := e.RequestReceivedTimestamp
	if !f.Since.IsZero() && t.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !t.Before(f.Until) {
		return false
	}
	if f.Resource != "" && (e.ObjectRef == nil || e.ObjectRef.Resource != f.Resource) {
		return false
	}
	if f.APIGroup != nil && (e.ObjectRef == nil || e.ObjectRef.APIGroup != *f.APIGroup) {
		return false
	}
	return true
}

// Select returns matching events.
func Select(events []Event, f Filter) []Event {
	var out []Event
	for _, e := range events {
		if f.match(e) {
			out = append(out, e)
		}
	}
	return out
}

// Writes counts mutating requests that matched the filter, successful or not.
func Writes(events []Event, f Filter) int {
	n := 0
	for _, e := range Select(events, f) {
		if e.IsWrite() {
			n++
		}
	}
	return n
}

// WritesPerMinute is Writes normalised by the filter's window.
func WritesPerMinute(events []Event, f Filter) float64 {
	if f.Since.IsZero() || f.Until.IsZero() || !f.Until.After(f.Since) {
		return float64(Writes(events, f))
	}
	return float64(Writes(events, f)) / f.Until.Sub(f.Since).Minutes()
}

// StatusWriteSplit counts successful writes to a resource via the main resource
// versus the status subresource.
type StatusWriteSplit struct {
	MainResource int `json:"mainResource"`
	Subresource  int `json:"statusSubresource"`
}

// StatusWrites splits the user's successful update/patch calls on resource.
func StatusWrites(events []Event, user, resource string) StatusWriteSplit {
	var s StatusWriteSplit
	for _, e := range Select(events, Filter{User: user, Resource: resource}) {
		if e.Verb != "update" && e.Verb != "patch" {
			continue
		}
		if c := e.Code(); c < 200 || c > 299 {
			continue
		}
		if e.ObjectRef.Subresource == "status" {
			s.Subresource++
		} else if e.ObjectRef.Subresource == "" {
			s.MainResource++
		}
	}
	return s
}

// Forbidden lists matching requests that got 403, as "verb group/resource".
func Forbidden(events []Event, f Filter) map[string]int {
	out := map[string]int{}
	for _, e := range Select(events, f) {
		if e.Code() != 403 {
			continue
		}
		key := e.Verb
		if e.ObjectRef != nil {
			g := e.ObjectRef.APIGroup
			if g == "" {
				g = "core"
			}
			res := e.ObjectRef.Resource
			if e.ObjectRef.Subresource != "" {
				res += "/" + e.ObjectRef.Subresource
			}
			key += " " + g + "/" + res
		} else {
			key += " " + e.RequestURI
		}
		out[key]++
	}
	return out
}

// Conflicts counts the user's requests that got 409.
func Conflicts(events []Event, f Filter) int {
	n := 0
	for _, e := range Select(events, f) {
		if e.Code() == 409 {
			n++
		}
	}
	return n
}
