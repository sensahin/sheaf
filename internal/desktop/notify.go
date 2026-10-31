package desktop

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// Alert is one desktop notification.
type Alert struct {
	Title string
	Body  string
}

// AlertFor returns the notification for an event, if the event means an
// agent needs you: a new item in the queue, a message to you, or an agent
// reporting that it is blocked.
func AlertFor(ev core.Event) (Alert, bool) {
	var d map[string]any
	json.Unmarshal(ev.Data, &d)
	s := func(k string) string { v, _ := d[k].(string); return v }
	switch ev.Type {
	case "approval.requested":
		title := ev.Actor + " needs your approval"
		switch s("kind") {
		case core.ApprovalDiff:
			title = ev.Actor + " wants a review"
		case core.ApprovalQuestion:
			title = ev.Actor + " has a question"
		}
		return Alert{title, s("summary")}, true
	case "message.sent":
		if s("to") != "human" {
			return Alert{}, false
		}
		kind := s("kind")
		if kind == "" {
			kind = "message"
		}
		return Alert{"New " + kind + " from " + ev.Actor, "Open Sheaf to read it."}, true
	case "agent.status":
		if s("state") != "blocked" {
			return Alert{}, false
		}
		body := s("note")
		if body == "" {
			body = "It is waiting for you."
		}
		return Alert{ev.Actor + " is blocked", body}, true
	}
	return Alert{}, false
}

// Watch follows the event log from now on and calls notify for every event
// that needs you, until ctx ends. It retries quietly while the daemon is
// away.
func Watch(ctx context.Context, c *api.Client, notify func(Alert)) {
	var after int64 = -1
	for ctx.Err() == nil {
		if after < 0 {
			var head struct {
				ID int64 `json:"id"`
			}
			if err := c.Call(ctx, "events.head", nil, &head); err != nil {
				sleep(ctx, 3*time.Second)
				continue
			}
			after = head.ID
		}
		var evs []core.Event
		err := c.Call(ctx, "events.list", api.EventsReq{After: after, Limit: 500, WaitSeconds: 25}, &evs)
		if err != nil {
			sleep(ctx, 3*time.Second)
			continue
		}
		for _, ev := range evs {
			after = ev.ID
			if a, ok := AlertFor(ev); ok {
				notify(a)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
