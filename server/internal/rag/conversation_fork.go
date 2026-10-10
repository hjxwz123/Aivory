package rag

import (
	"context"
	"errors"
)

func (s *Service) PrepareConversationForkVectors() (func(context.Context, string, map[string]string) error, func(), error) {
	if !s.vec.Enabled() {
		return nil, func() {}, nil
	}
	if preparer, ok := s.vec.(interface {
		PrepareConversationFork() (func(context.Context, string, map[string]string) error, func())
	}); ok {
		copyVectors, release := preparer.PrepareConversationFork()
		return copyVectors, release, nil
	}
	if copier, ok := s.vec.(interface {
		CopyConversationPoints(context.Context, string, map[string]string) error
	}); ok {
		return copier.CopyConversationPoints, func() {}, nil
	}
	return nil, func() {}, errors.New("vector backend does not support conversation forks")
}
