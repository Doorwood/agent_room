package answerwindow

import (
	"agent_romm/internal/room"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskMutationRequiresSameOriginAndRoommate(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	calls := 0
	w.EnableTasks(func(context.Context, room.TaskRequest) (room.TaskReply, error) { calls++; return room.TaskReply{}, nil })
	for _, role := range []string{"roommate", "visitor", "asker"} {
		w.userRole = role
		for _, origin := range []string{"", "https://evil.test", "http://" + w.listener.Addr().String()} {
			r := httptest.NewRequest("POST", w.URL()+"tasks", strings.NewReader(`{"action":"complete","requestId":"00000000000000000000000000000001","taskId":1,"expectedRevision":1}`))
			r.Header.Set("Origin", origin)
			out := httptest.NewRecorder()
			w.serve(out, r)
			allowed := role == "roommate" && origin == "http://"+w.listener.Addr().String()
			if (out.Code == 200) != allowed {
				t.Fatal(role, origin, out.Code, out.Body.String())
			}
		}
	}
	if calls != 1 {
		t.Fatal("forged request reached host", calls)
	}
}
