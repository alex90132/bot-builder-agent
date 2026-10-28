package agent

import (
	"context"
	"encoding/json"
)

// Remote — публичное имя инструмента и сервер, которому принадлежит исходное имя.
type Remote struct {
	PublicName  string
	Original    string
	ServerID    int64
	ServerName  string
	Builder     bool
	Revision    string
	Description string
	Schema      json.RawMessage
}

// RemoteSet — инструменты включённых серверов одного чата.
type RemoteSet struct {
	Tools      []Remote
	Failures   []string
	HasBuilder bool
}

// Remotes собирает инструменты всех включённых серверов чата.
type Remotes interface {
	Catalog(ctx context.Context, userID int64) (RemoteSet, error)
	Call(ctx context.Context, userID int64, publicName string, args json.RawMessage) (string, bool, error)
}
