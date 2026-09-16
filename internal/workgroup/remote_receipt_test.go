package workgroup

import (
	"strings"
	"testing"
)

func TestReceivedJobIsNotPolledAgainWhileHostSavesWorkspace(t *testing.T) {
	b := NewRemoteBroker()
	inv, err := b.Handle(7, WorkerRequest{Action: "invite", ID: strings.Repeat("a", 32), Provider: "codex", Mode: "review"})
	if err != nil {
		t.Fatal(err)
	}
	j := &remoteJob{id: "job", claimed: true, done: make(chan Result, 1)}
	b.workers[inv.ID].job = j
	req := WorkerRequest{Action: "result", ID: inv.ID, JobID: j.id, Text: "done"}
	if _, err := b.Handle(7, req); err != nil {
		t.Fatal(err)
	}
	// The provider has consumed the result but still owns the job while saving
	// the workspace. A worker that got the acknowledgement has cleared its job ID.
	<-j.done
	poll, err := b.Handle(7, WorkerRequest{Action: "poll", ID: inv.ID})
	if err != nil || poll.JobID != "" || poll.Assignment != nil {
		t.Fatalf("acknowledged job advertised as unclaimed: %+v, %v", poll, err)
	}
	if _, err := b.Handle(7, req); err == nil {
		t.Fatal("received result accepted twice after channel drained")
	}
	if b.workers[inv.ID].job != j {
		t.Fatal("host ownership released before saving result")
	}
}
