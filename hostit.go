package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// hostitClient reads connections from hostit's container API, so a source can take its database
// credential from a hostit connection instead of the config ("hostit_connection"). The app is
// identified by its uid on hostit's side; there is nothing to authenticate with.
type hostitClient struct {
	url  string
	http *http.Client
}

// dsn fills in the host of a URL-form DSN that has none from the connection's note
// ("note=postgresql://host:port", the convention of the ntfy-db connection). A DSN with a host is
// returned as is.
func (h *hostitClient) dsn(ctx context.Context, slug, dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Host != "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return dsn, nil
	}
	var conns []struct {
		Slug string `json:"slug"`
		Meta string `json:"meta"`
	}
	if err := h.get(ctx, "/api/container/connections", &conns); err != nil {
		return "", err
	}
	for _, c := range conns {
		if c.Slug != slug {
			continue
		}
		note, err := url.Parse(strings.TrimPrefix(c.Meta, "note="))
		if err != nil || note.Host == "" {
			return "", fmt.Errorf("hostit connection %q: the DSN has no host and the connection's note is not a postgres URL", slug)
		}
		u.Host = note.Host
		return u.String(), nil
	}
	return "", fmt.Errorf("hostit connection %q is not granted to this app", slug)
}

// token returns the connection's credential, here the database password.
func (h *hostitClient) token(ctx context.Context, slug string) (string, error) {
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := h.get(ctx, "/api/container/connections/"+url.PathEscape(slug)+"/token", &body); err != nil {
		return "", err
	}
	return body.AccessToken, nil
}

func (h *hostitClient) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url+path, nil)
	if err != nil {
		return err
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return fmt.Errorf("hostit container API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return fmt.Errorf("hostit container API: %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
