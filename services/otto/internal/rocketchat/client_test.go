package rocketchat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliveryReplaysWithoutDuplicateAndRejectsWrongRoom(t *testing.T) {
	var stored *Message
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-Id") != "bot" || r.Header.Get("X-Auth-Token") != "test-token" {
			t.Error("missing bot authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/chat.getMessage":
			if stored == nil {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": stored})
		case "/api/v1/chat.sendMessage":
			var in struct {
				Message Message `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			in.Message.User.ID = "bot"
			stored = &in.Message
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": stored})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c, err := NewClient(Config{URL: server.URL, UserID: "bot", Token: "test-token", RoomID: "support", HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	m := Message{ID: "stable", RoomID: "support", Text: "Hello", ThreadID: "case"}
	for range 2 {
		if err := c.Send(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 || stored.ThreadID != "case" {
		t.Fatalf("posts=%d message=%+v", posts, stored)
	}
	stored.RoomID = "another-product"
	if err := c.Send(t.Context(), m); err == nil {
		t.Fatal("accepted mismatching room")
	}
}

func TestClientRequiresHTTPSAndCompleteCredentials(t *testing.T) {
	for _, cfg := range []Config{{URL: "http://chat.test", UserID: "b", Token: "t", RoomID: "r"}, {URL: "https://chat.test"}, {URL: "https://chat.test/path", UserID: "b", Token: "t", RoomID: "r"}} {
		if _, err := NewClient(cfg); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}
