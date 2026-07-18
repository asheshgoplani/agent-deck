package model

type Project struct {
	Path     string
	Name     string
	Tool     string
	Sessions []Session
}
