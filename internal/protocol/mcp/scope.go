package mcp

import (
	"context"
	"errors"
	"sync"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// ScopeResolver resolves the stable application scope the MCP server serves.
// Scope bootstrap is a client concern, not an Agent tool: the Agent never asks
// dMem to create a scope.
type ScopeResolver interface {
	ResolveScope(ctx context.Context) (string, error)
}

// ScopeEnsurer is the narrow client port used for scope bootstrap.
type ScopeEnsurer interface {
	EnsureScope(context.Context, client.Call, toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error)
}

// ConfiguredScope resolves one scope by stable external identity and caches the
// application scope id. Resolution is retried on the next call after a failure
// so a temporarily unavailable daemon does not permanently break the session.
type ConfiguredScope struct {
	Client      ScopeEnsurer
	ScopeType   string
	ExternalID  string
	DisplayName string
	ParentID    *string

	mutex    sync.Mutex
	resolved string
}

// ResolveScope returns the cached scope id, ensuring the scope on first use.
func (s *ConfiguredScope) ResolveScope(ctx context.Context) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.resolved != "" {
		return s.resolved, nil
	}
	if s.Client == nil {
		return "", errors.New("mcp: scope client is required")
	}
	displayName := s.DisplayName
	if displayName == "" {
		displayName = s.ExternalID
	}
	result, err := s.Client.EnsureScope(ctx, client.Call{CorrelationID: "dmem-mcp-bootstrap"}, toolcontract.ScopeEnsureInput{
		ParentScopeID: s.ParentID,
		ScopeType:     s.ScopeType,
		ExternalID:    s.ExternalID,
		DisplayName:   displayName,
	})
	if err != nil {
		return "", err
	}
	if result.Scope.ScopeID == "" {
		return "", errors.New("mcp: scope ensure returned no scope id")
	}
	s.resolved = result.Scope.ScopeID
	return s.resolved, nil
}
