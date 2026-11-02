package core

import (
	"context"
	"errors"
	"testing"
)

func TestAutomaticStatusDoesNotReopenFinishedOrHandedOffWork(t *testing.T) {
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")
	addAgent(t, s, "reviewer")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, HumanCaller, TaskSpec{Title: "Read only report", Owner: b.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReportStatus(ctx, b, StatusReport{State: StateDone, Task: task.ID}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{StateWorking, StateDone, StateBlocked, StateIdle} {
		if err = s.ReportStatus(ctx, b, StatusReport{State: state, Task: task.ID, Lifecycle: true}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetTask(ctx, task.ID)
		if got.Status != TaskDone {
			t.Fatalf("automatic %s reopened task: %+v", state, got)
		}
	}
	next, _ := s.CreateTask(ctx, HumanCaller, TaskSpec{Title: "Review", Owner: b.Name})
	if _, err = s.Handoff(ctx, b, HandoffRequest{Task: next.ID, To: "reviewer", Note: "Please review"}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReportStatus(ctx, b, StatusReport{State: StateWorking, Task: next.ID, Lifecycle: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, next.ID)
	if got.Owner != "reviewer" || got.Status != TaskOpen {
		t.Fatalf("changed handed-off task: %+v", got)
	}
	agent, _ := s.GetAgent(ctx, b.Name)
	if agent.State != StateWorking {
		t.Fatal("lifecycle observation did not update the agent")
	}
	if err = s.ReportStatus(ctx, b, StatusReport{State: StateDone, Task: next.ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manual foreign task update: %v", err)
	}
}

func TestDecisionFallbackRemainsUnreadUntilDeliveredToItsAgent(t *testing.T) {
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")
	ctx := context.Background()
	first, _ := s.RequestApproval(ctx, b, ApprovalRequest{Summary: "First"})
	second, _ := s.RequestApproval(ctx, b, ApprovalRequest{Summary: "Second"})
	for _, a := range []Approval{first, second} {
		if _, err := s.Decide(ctx, HumanCaller, DecideRequest{ID: a.ID, Status: ApprovalApproved}); err != nil {
			t.Fatal(err)
		}
	}
	// A human viewing the decision must not consume the agent's delivery.
	if _, err := s.WaitApproval(ctx, HumanCaller, first.ID, 0); err != nil {
		t.Fatal(err)
	}
	in, _ := s.Inbox(ctx, b, InboxRequest{Peek: true})
	if len(in) != 2 {
		t.Fatalf("lost fallback decisions: %+v", in)
	}
	// Receiving one decision through wait only consumes that exact decision.
	if _, err := s.WaitApproval(ctx, b, first.ID, 0); err != nil {
		t.Fatal(err)
	}
	in, _ = s.Inbox(ctx, b, InboxRequest{Peek: true})
	if len(in) != 1 || in[0].Body[:len(second.ID)] != second.ID {
		t.Fatalf("wrong delivery consumed: %+v", in)
	}
}
