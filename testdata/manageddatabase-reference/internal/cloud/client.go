// Package cloud is a small client for the fake cloud database API (CLOUD_API.md).
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// ErrNotFound and ErrConflict map the API's 404 and 409.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Database is the API's database object.
type Database struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	SizeGB   int32  `json:"sizeGB"`
	Status   string `json:"status"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // only on create
}

// Client talks to $FAKECLOUD_URL.
type Client struct {
	Base string
	HTTP *http.Client
}

// FromEnv builds a client from FAKECLOUD_URL.
func FromEnv() (*Client, error) {
	base := os.Getenv("FAKECLOUD_URL")
	if base == "" {
		return nil, errors.New("FAKECLOUD_URL is not set")
	}
	return &Client{Base: base, HTTP: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusConflict:
		return ErrConflict
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Create creates a database. The returned object carries the password.
func (c *Client) Create(ctx context.Context, name, engine string, sizeGB int32) (*Database, error) {
	db := &Database{}
	err := c.do(ctx, http.MethodPost, "/v1/databases", map[string]any{"name": name, "engine": engine, "sizeGB": sizeGB}, db)
	return db, err
}

// Get fetches a database by ID.
func (c *Client) Get(ctx context.Context, id string) (*Database, error) {
	db := &Database{}
	err := c.do(ctx, http.MethodGet, "/v1/databases/"+url.PathEscape(id), nil, db)
	return db, err
}

// FindByName returns the database with that name, or ErrNotFound.
func (c *Client) FindByName(ctx context.Context, name string) (*Database, error) {
	var out struct {
		Items []Database `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/databases?name="+url.QueryEscape(name), nil, &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	return &out.Items[0], nil
}

// Resize changes the size. Returns ErrConflict if the database isn't available.
func (c *Client) Resize(ctx context.Context, id string, sizeGB int32) error {
	return c.do(ctx, http.MethodPatch, "/v1/databases/"+url.PathEscape(id), map[string]any{"sizeGB": sizeGB}, nil)
}

// Delete starts deletion.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/databases/"+url.PathEscape(id), nil, nil)
}

// ResetPassword rotates and returns a new password.
func (c *Client) ResetPassword(ctx context.Context, id string) (string, error) {
	var out struct {
		Password string `json:"password"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/databases/"+url.PathEscape(id)+"/reset-password", nil, &out)
	return out.Password, err
}
