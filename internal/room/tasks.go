package room

// ProjectTask is a project-scoped review unit, separate from a Codex turn.
type ProjectTask struct {
	ID             int64        `json:"id"`
	Number         int64        `json:"number"`
	Title          string       `json:"title"`
	Acceptance     string       `json:"acceptance"`
	SourceSeq      int64        `json:"sourceSeq"`
	SourceText     string       `json:"sourceText,omitempty"`
	SourceClientID string       `json:"sourceClientId"`
	State          string       `json:"state"`
	Revision       int64        `json:"revision"`
	CreatedAt      string       `json:"createdAt"`
	UpdatedAt      string       `json:"updatedAt"`
	CompletedAt    string       `json:"completedAt,omitempty"`
	CompletedBy    string       `json:"completedBy,omitempty"`
	Runs           []TaskRun    `json:"runs,omitempty"`
	History        []TaskChange `json:"history,omitempty"`
}
type TaskRun struct {
	MessageID   int64        `json:"messageId"`
	ClientID    string       `json:"clientId"`
	Turn        string       `json:"turn"`
	State       string       `json:"state"`
	Text        string       `json:"text"`
	CreatedAt   string       `json:"createdAt"`
	StartedAt   string       `json:"startedAt,omitempty"`
	CompletedAt string       `json:"completedAt,omitempty"`
	Answers     []TaskAnswer `json:"answers,omitempty"`
}
type TaskAnswer struct {
	Text string `json:"text"`
	Time string `json:"time"`
}
type TaskChange struct {
	Action string `json:"action"`
	Actor  string `json:"actor"`
	Time   string `json:"time"`
	Note   string `json:"note,omitempty"`
}
type TaskRequest struct {
	Action           string `json:"action"`
	RequestID        string `json:"requestId,omitempty"`
	TaskID           int64  `json:"taskId,omitempty"`
	SourceSeq        int64  `json:"sourceSeq,omitempty"`
	Title            string `json:"title,omitempty"`
	Acceptance       string `json:"acceptance,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
	Note             string `json:"note,omitempty"`
	Before           int64  `json:"before,omitempty"`
}
type TaskReply struct {
	Tasks []ProjectTask `json:"tasks,omitempty"`
	Task  *ProjectTask  `json:"task,omitempty"`
	More  bool          `json:"more"`
	Error string        `json:"error,omitempty"`
}
