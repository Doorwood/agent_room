package dashboard

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"agent_romm/internal/answerwindow"
	"agent_romm/internal/client"
	"agent_romm/internal/network"
)

type Update func(status, detail, url string)
type Connector func(context.Context, Room, Update) error

type approvalOutput struct{ update Update }

func (w approvalOutput) Write(b []byte) (int, error) {
	text := string(b)
	if strings.Contains(text, "Waiting for host approval") {
		w.update("pending", strings.TrimSpace(text), "")
	}
	return len(b), nil
}
func (c Catalog) Connect(ctx context.Context, r Room, update Update) error {
	credential, err := network.CredentialFor(c.credentialDir(), r.Address, r.Session, r.Name)
	if err != nil {
		return err
	}
	launcher := network.Launcher{Credential: credential}
	if err = launcher.WaitApproval(ctx, approvalOutput{update}); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	window, err := answerwindow.Start()
	if err != nil {
		return err
	}
	defer window.Close()
	window.EnableUploads(launcher.Upload)
	window.EnableDownloads(launcher.Download)
	window.EnableQuestions(launcher.Query)
	window.EnableResources(launcher.Resources)
	window.EnableTasks(launcher.Tasks)
	window.DraftDirectory(filepath.Join(c.Config, "agent_room", "drafts"))
	window.Metadata(r.Address, r.Session, r.Name)
	window.Project(r.Project)
	update("connecting", "正在同步 room", window.URL())
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	deps := client.Deps{Launcher: launcher, Cursors: &client.ReplayCursors{}, OnAnswer: window.Add, OnEvent: window.Event, OnMembers: window.Members, Submissions: window.EnableChat(), OnRoom: window.Room, OnActiveTurn: window.ActiveTurn, OnQueue: window.Queue, OnProject: func(project string) {
		window.Project(project)
		if err := c.RememberProject(r.Address, r.Session, project); err != nil {
			update("connecting", "项目名称缓存失败："+client.SafeText(err.Error()), window.URL())
		}
	}, OnConnection: func(connected bool) {
		window.Connection(connected)
		if connected {
			window.RefreshRole(ctx)
		}
		if connected {
			update("connected", "", window.URL())
		} else {
			update("reconnecting", "正在重连 host", window.URL())
		}
	}}
	return client.New(deps).Run(ctx, r.ID, reader, io.Discard, io.Discard)
}
