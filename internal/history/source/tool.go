package source

import "github.com/asheshgoplani/agent-deck/internal/history/model"

type Tool interface {
	Name() string
	Discover() ([]model.Project, error)
	Command(s model.Session, fork bool) string
	Delete(s model.Session) error
	RefreshStatus(projects []model.Project)
}
