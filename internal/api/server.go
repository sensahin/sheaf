// Package api exposes the core service over HTTP on a Unix socket.
//
// Every call is POST /v1/<method> with a JSON body and a bearer token. The
// token decides who the caller is: the human owner or one agent. There is no
// unauthenticated method, so a process that can reach the socket still cannot
// act without a token.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// Server serves the API.
type Server struct {
	svc *core.Service
	mux *http.ServeMux
}

type handler func(ctx context.Context, c core.Caller, body json.RawMessage) (any, error)

// NewServer returns a server for svc.
func NewServer(svc *core.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	for name, h := range s.methods() {
		s.mux.Handle("POST /v1/"+name, s.wrap(h))
	}
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenUnix serves on a Unix socket at path, readable only by this user,
// until ctx ends.
func (s *Server) ListenUnix(ctx context.Context, path string) error {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return errors.New("a daemon is already listening on " + path)
	}
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	err = srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) wrap(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, err := s.svc.Authenticate(r.Context(), token)
		if err != nil {
			writeErr(w, err)
			return
		}
		var body json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil && err.Error() != "EOF" {
			writeErr(w, core.ErrInvalid)
			return
		}
		out, err := h(r.Context(), c, body)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
}

type errorBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, core.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, core.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, core.ErrNotFound):
		code = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errorBody{err.Error()})
}

// decode is a helper for handlers that take a typed request.
func decode[T any](body json.RawMessage) (T, error) {
	var v T
	if len(body) == 0 || string(body) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, errors.Join(core.ErrInvalid, err)
	}
	return v, nil
}

// typed adapts a function on a request type to a handler.
func typed[T any](f func(ctx context.Context, c core.Caller, req T) (any, error)) handler {
	return func(ctx context.Context, c core.Caller, body json.RawMessage) (any, error) {
		req, err := decode[T](body)
		if err != nil {
			return nil, err
		}
		return f(ctx, c, req)
	}
}

// Request bodies that are not core types.
type (
	NameReq struct {
		Name string `json:"name"`
	}
	IDReq struct {
		ID string `json:"id"`
	}
	PolicyReq struct {
		Name   string      `json:"name"`
		Policy core.Policy `json:"policy"`
	}
	TaskStatusReq struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	ApprovalsReq struct {
		Status string `json:"status,omitempty"`
	}
	WaitReq struct {
		ID          string `json:"id"`
		WaitSeconds int    `json:"wait_seconds,omitempty"`
	}
	EventsReq struct {
		After       int64 `json:"after"`
		Limit       int   `json:"limit,omitempty"`
		WaitSeconds int   `json:"wait_seconds,omitempty"`
	}
	AddAgentResp struct {
		Agent core.Agent `json:"agent"`
		Token string     `json:"token"`
	}
	TokenResp struct {
		Token string `json:"token"`
	}
	OK struct {
		OK bool `json:"ok"`
	}
)

// maxWait caps long polls so a client cannot pin a connection forever.
const maxWait = 15 * time.Minute

func waitDur(sec int) time.Duration {
	d := time.Duration(sec) * time.Second
	if d > maxWait {
		d = maxWait
	}
	return d
}

func (s *Server) methods() map[string]handler {
	svc := s.svc
	return map[string]handler{
		"whoami": func(ctx context.Context, c core.Caller, _ json.RawMessage) (any, error) { return c, nil },

		"agent.add": typed(func(ctx context.Context, c core.Caller, r core.AgentSpec) (any, error) {
			a, tok, err := svc.AddAgent(ctx, c, r)
			return AddAgentResp{a, tok}, err
		}),
		"agent.list": func(ctx context.Context, c core.Caller, _ json.RawMessage) (any, error) { return svc.ListAgents(ctx) },
		"agent.get": typed(func(ctx context.Context, c core.Caller, r NameReq) (any, error) {
			return svc.GetAgent(ctx, r.Name)
		}),
		"agent.remove": typed(func(ctx context.Context, c core.Caller, r NameReq) (any, error) {
			return OK{true}, svc.RemoveAgent(ctx, c, r.Name)
		}),
		"agent.rotate_token": typed(func(ctx context.Context, c core.Caller, r NameReq) (any, error) {
			tok, err := svc.RotateToken(ctx, c, r.Name)
			return TokenResp{tok}, err
		}),
		"agent.set_policy": typed(func(ctx context.Context, c core.Caller, r PolicyReq) (any, error) {
			return OK{true}, svc.SetPolicy(ctx, c, r.Name, r.Policy)
		}),
		"agent.report_status": typed(func(ctx context.Context, c core.Caller, r core.StatusReport) (any, error) {
			return OK{true}, svc.ReportStatus(ctx, c, r)
		}),

		"task.create": typed(func(ctx context.Context, c core.Caller, r core.TaskSpec) (any, error) {
			return svc.CreateTask(ctx, c, r)
		}),
		"task.list": typed(func(ctx context.Context, c core.Caller, r core.TaskFilter) (any, error) {
			return svc.ListTasks(ctx, r)
		}),
		"task.get": typed(func(ctx context.Context, c core.Caller, r IDReq) (any, error) {
			return svc.GetTask(ctx, r.ID)
		}),
		"task.set_status": typed(func(ctx context.Context, c core.Caller, r TaskStatusReq) (any, error) {
			return OK{true}, svc.SetTaskStatus(ctx, c, r.ID, r.Status)
		}),
		"task.handoff": typed(func(ctx context.Context, c core.Caller, r core.HandoffRequest) (any, error) {
			return svc.Handoff(ctx, c, r)
		}),
		"task.resume_plan": func(ctx context.Context, c core.Caller, _ json.RawMessage) (any, error) {
			return svc.ResumePlan(ctx)
		},

		"message.send": typed(func(ctx context.Context, c core.Caller, r core.SendRequest) (any, error) {
			return svc.Send(ctx, c, r)
		}),
		"message.inbox": typed(func(ctx context.Context, c core.Caller, r core.InboxRequest) (any, error) {
			r.WaitSeconds = int(waitDur(r.WaitSeconds) / time.Second)
			return svc.Inbox(ctx, c, r)
		}),
		"message.status": typed(func(ctx context.Context, c core.Caller, r IDReq) (any, error) {
			return svc.MessageStatus(ctx, c, r.ID)
		}),

		"approval.request": typed(func(ctx context.Context, c core.Caller, r core.ApprovalRequest) (any, error) {
			return svc.RequestApproval(ctx, c, r)
		}),
		"approval.list": typed(func(ctx context.Context, c core.Caller, r ApprovalsReq) (any, error) {
			if !c.IsHuman() {
				return nil, core.ErrForbidden
			}
			return svc.ListApprovals(ctx, r.Status)
		}),
		"approval.get": typed(func(ctx context.Context, c core.Caller, r IDReq) (any, error) {
			return svc.GetApproval(ctx, c, r.ID)
		}),
		"approval.wait": typed(func(ctx context.Context, c core.Caller, r WaitReq) (any, error) {
			return svc.WaitApproval(ctx, c, r.ID, waitDur(r.WaitSeconds))
		}),
		"approval.decide": typed(func(ctx context.Context, c core.Caller, r core.DecideRequest) (any, error) {
			return svc.Decide(ctx, c, r)
		}),

		"policy.check": typed(func(ctx context.Context, c core.Caller, r core.CheckRequest) (any, error) {
			return svc.Check(ctx, c, r)
		}),

		"events.list": typed(func(ctx context.Context, c core.Caller, r EventsReq) (any, error) {
			if !c.IsHuman() {
				return nil, core.ErrForbidden
			}
			return svc.Events(ctx, r.After, r.Limit, waitDur(r.WaitSeconds))
		}),
	}
}
