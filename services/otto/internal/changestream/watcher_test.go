package changestream

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/tesserix/slm-support-platform/services/otto/internal/event"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
)

// capturingQueue records what the watcher decided to publish.
type capturingQueue struct{ events []event.QueueEvent }

func (c *capturingQueue) Publish(ev event.QueueEvent) { c.events = append(c.events, ev) }

// noopHub satisfies Broadcaster without doing anything.
type noopHub struct{}

func (noopHub) Broadcast(string, hub.Envelope)          {}
func (noopHub) BroadcastInbox(string, string, hub.Envelope) {}

func newWatcher() (*Watcher, *capturingQueue) {
	q := &capturingQueue{}
	return &Watcher{Hub: noopHub{}, Queue: q}, q
}

func baseDoc() convDoc {
	d := convDoc{
		ID:        "conv-1",
		CaseID:    "CS-1",
		TenantID:  "homechef",
		StoreID:   "default",
		Status:    "active",
		UpdatedAt: time.Now().UTC(),
	}
	return d
}

func updatedFields(t *testing.T, m bson.M) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(m)
	if err != nil {
		t.Fatalf("marshal updatedFields: %v", err)
	}
	return raw
}

// The AI answering flips a thread to `active` with no assignee. Publishing
// "accepted" there tells consumers a human took the chat, cancelling their
// wait-for-staff timers — so it must stay silent.
func TestPublishQueueTransition_ActiveWithoutAssigneeIsNotAccepted(t *testing.T) {
	w, q := newWatcher()
	doc := baseDoc() // Assignee nil

	w.publishQueueTransition("update", updatedFields(t, bson.M{"status": "active"}), doc)

	if len(q.events) != 0 {
		t.Fatalf("expected no event for an unassigned active flip, got %+v", q.events)
	}
}

// A real staff accept carries an assignee.
func TestPublishQueueTransition_ActiveWithAssigneeIsAccepted(t *testing.T) {
	w, q := newWatcher()
	doc := baseDoc()
	doc.Assignee = &struct {
		Name string `bson:"name"`
	}{Name: "Sam"}

	w.publishQueueTransition("update", updatedFields(t, bson.M{"status": "active"}), doc)

	if len(q.events) != 1 || q.events[0].Event != event.QueueAccepted {
		t.Fatalf("expected one accepted event, got %+v", q.events)
	}
	if q.events[0].AssigneeName != "Sam" {
		t.Fatalf("expected the assignee name on the event, got %q", q.events[0].AssigneeName)
	}
}

func TestPublishQueueTransition_EscalationAndClose(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fields  bson.M
		status  string
		want    string
	}{
		{"escalated", bson.M{"needs_human": true}, "pending", event.QueueEscalated},
		{"closed", bson.M{"status": "closed"}, "closed", event.QueueClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, q := newWatcher()
			doc := baseDoc()
			doc.Status = tc.status

			w.publishQueueTransition("update", updatedFields(t, tc.fields), doc)

			if len(q.events) != 1 || q.events[0].Event != tc.want {
				t.Fatalf("expected one %s event, got %+v", tc.want, q.events)
			}
		})
	}
}

// Counter bumps (message_count, unread counts) are not lifecycle changes.
func TestPublishQueueTransition_IgnoresNonLifecycleUpdates(t *testing.T) {
	w, q := newWatcher()

	w.publishQueueTransition("update", updatedFields(t, bson.M{"message_count": 4}), baseDoc())

	if len(q.events) != 0 {
		t.Fatalf("expected no event for a counter bump, got %+v", q.events)
	}
}

func TestPublishQueueTransition_InsertIsCreated(t *testing.T) {
	w, q := newWatcher()
	doc := baseDoc()
	doc.Status = "pending"

	w.publishQueueTransition("insert", nil, doc)

	if len(q.events) != 1 || q.events[0].Event != event.QueueCreated {
		t.Fatalf("expected one created event, got %+v", q.events)
	}
}
