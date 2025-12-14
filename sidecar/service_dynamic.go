package sidecar

import (
	"context"
	"sync"

	servertypes "github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

// DynamicService is a thin wrapper that allows swapping the underlying
// Service implementation at runtime (e.g. when drand is started/stopped based
// on on-chain state).
type DynamicService struct {
	mu   sync.RWMutex
	svc  Service
	info *servertypes.QueryInfoResponse
}

func NewDynamicService(initialInfo *servertypes.QueryInfoResponse) *DynamicService {
	return &DynamicService{info: cloneInfoResponse(initialInfo)}
}

func (s *DynamicService) SetService(svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.svc = svc
}

func (s *DynamicService) SetInfo(info *servertypes.QueryInfoResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info = cloneInfoResponse(info)
}

func (s *DynamicService) Randomness(ctx context.Context, round uint64) (*servertypes.QueryRandomnessResponse, error) {
	s.mu.RLock()
	svc := s.svc
	s.mu.RUnlock()

	if svc == nil {
		return nil, ErrServiceUnavailable
	}

	return svc.Randomness(ctx, round)
}

func (s *DynamicService) Info(context.Context) (*servertypes.QueryInfoResponse, error) {
	s.mu.RLock()
	info := s.info
	s.mu.RUnlock()

	if info == nil {
		return nil, ErrServiceUnavailable
	}

	return cloneInfoResponse(info), nil
}

func cloneInfoResponse(info *servertypes.QueryInfoResponse) *servertypes.QueryInfoResponse {
	if info == nil {
		return nil
	}

	out := &servertypes.QueryInfoResponse{
		PeriodSeconds:  info.PeriodSeconds,
		GenesisUnixSec: info.GenesisUnixSec,
	}
	if len(info.ChainHash) > 0 {
		out.ChainHash = append([]byte(nil), info.ChainHash...)
	}
	if len(info.PublicKey) > 0 {
		out.PublicKey = append([]byte(nil), info.PublicKey...)
	}
	return out
}
