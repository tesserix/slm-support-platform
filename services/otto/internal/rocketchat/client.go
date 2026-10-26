// Package rocketchat bridges mark8ly platform support to its restricted channel.
package rocketchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config contains server-only bot settings sourced from OpenBao.
type Config struct {
	URL, UserID, Token, RoomID string
	StaffIDs                   []string
	HTTP                       *http.Client
}

// Message is the allowlisted Rocket.Chat wire representation.
type Message struct {
	ID       string `json:"_id"`
	RoomID   string `json:"rid"`
	ThreadID string `json:"tmid,omitempty"`
	Text     string `json:"msg"`
	Type     string `json:"t,omitempty"`
	User     struct {
		ID       string `json:"_id"`
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"u,omitempty"`
	Timestamp   time.Time         `json:"ts,omitempty"`
	EditedAt    *time.Time        `json:"editedAt,omitempty"`
	Attachments []json.RawMessage `json:"attachments,omitempty"`
}

type response struct {
	Success  bool      `json:"success"`
	Message  Message   `json:"message"`
	Messages []Message `json:"messages"`
}

// Client bounds and authenticates requests to one HTTPS workspace.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient rejects incomplete credentials and origins that can redirect secrets.
func NewClient(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.TrimSpace(cfg.UserID) == "" || strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.RoomID) == "" {
		return nil, errors.New("rocketchat: complete HTTPS bot configuration required")
	}
	hc := http.Client{Timeout: 5 * time.Second}
	if cfg.HTTP != nil {
		hc = *cfg.HTTP
		hc.Timeout = 5 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &hc}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (response, int, error) {
	var out response
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return out, 0, errors.New("rocketchat: encode request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.URL, "/")+"/api/v1/"+path, bytes.NewReader(data))
	if err != nil {
		return out, 0, errors.New("rocketchat: invalid request")
	}
	req.Header.Set("X-User-Id", c.cfg.UserID)
	req.Header.Set("X-Auth-Token", c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return out, 0, errors.New("rocketchat: transport failed")
	}
	defer func() { _ = res.Body.Close() }() // Response closure cannot change remote delivery.
	if res.StatusCode != http.StatusOK {
		return out, res.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return out, res.StatusCode, errors.New("rocketchat: invalid response")
	}
	return out, res.StatusCode, nil
}

// Send verifies a prior delivery before retrying a deterministic message ID.
func (c *Client) Send(ctx context.Context, m Message) error {
	if m.RoomID != c.cfg.RoomID || m.ID == "" || m.Text == "" {
		return errors.New("rocketchat: invalid target")
	}
	prior, status, err := c.request(ctx, http.MethodGet, "chat.getMessage?msgId="+url.QueryEscape(m.ID), nil)
	if err != nil {
		return err
	}
	matches := func(got Message) bool {
		return got.ID == m.ID && got.RoomID == m.RoomID && got.ThreadID == m.ThreadID && got.Text == m.Text && got.User.ID == c.cfg.UserID
	}
	if status == 200 && prior.Success {
		if matches(prior.Message) {
			return nil
		}
		return errors.New("rocketchat: existing message mismatch")
	}
	if status != 400 && status != 404 {
		return fmt.Errorf("rocketchat: lookup HTTP %d", status)
	}
	wire := map[string]string{"_id": m.ID, "rid": m.RoomID, "msg": m.Text}
	if m.ThreadID != "" {
		wire["tmid"] = m.ThreadID
	}
	sent, status, err := c.request(ctx, http.MethodPost, "chat.sendMessage", map[string]any{"message": wire, "previewUrls": []string{}})
	if err != nil {
		return err
	}
	if status != 200 || !sent.Success || !matches(sent.Message) {
		return fmt.Errorf("rocketchat: send unconfirmed HTTP %d", status)
	}
	return nil
}

// Thread reads one bounded page; the caller validates every returned room/author.
func (c *Client) Thread(ctx context.Context, id string, offset int) ([]Message, error) {
	q := url.Values{"tmid": {id}, "count": {"100"}, "offset": {fmt.Sprint(offset)}, "sort": {`{"ts":1,"_id":1}`}}
	out, status, err := c.request(ctx, http.MethodGet, "chat.getThreadMessages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 || !out.Success {
		return nil, fmt.Errorf("rocketchat: thread HTTP %d", status)
	}
	return out.Messages, nil
}
