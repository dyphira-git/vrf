package sidecar

import (
	"context"
	"sync"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	scerror "github.com/vexxvakan/vrf/sidecar/errors"
)

// Service provides the sidecar's core VRF API (drand beacons + chain info).
//
// Implementations may be swapped at runtime (e.g. when drand is started/stopped
// based on on-chain state).
type Service interface {
	Randomness(ctx context.Context, round uint64) (*sidecarv1.QueryRandomnessResponse, error)
	Info(ctx context.Context) (*sidecarv1.QueryInfoResponse, error)
}

// DynamicService is a thin wrapper that allows swapping the underlying Service
// implementation at runtime (e.g. when drand is started/stopped based on
// on-chain state).
type DynamicService struct {
	mu   sync.RWMutex
	svc  Service
	info *sidecarv1.QueryInfoResponse
}

func NewDynamicService(initialInfo *sidecarv1.QueryInfoResponse) *DynamicService {
	return &DynamicService{info: cloneInfoResponse(initialInfo)}
}

func (s *DynamicService) SetService(svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.svc = svc
}

func (s *DynamicService) SetInfo(info *sidecarv1.QueryInfoResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info = cloneInfoResponse(info)
}

func (s *DynamicService) Randomness(ctx context.Context, round uint64) (*sidecarv1.QueryRandomnessResponse, error) {
	s.mu.RLock()
	svc := s.svc
	s.mu.RUnlock()

	if svc == nil {
		return nil, scerror.ErrServiceUnavailable
	}

	return svc.Randomness(ctx, round)
}

func (s *DynamicService) Info(ctx context.Context) (*sidecarv1.QueryInfoResponse, error) {
	s.mu.RLock()
	info := cloneInfoResponse(s.info)
	svc := s.svc
	s.mu.RUnlock()

	if info == nil {
		// Fall back to the backing service if available.
		if svc == nil {
			return nil, scerror.ErrServiceUnavailable
		}
		return svc.Info(ctx)
	}

	return info, nil
}

func cloneInfoResponse(info *sidecarv1.QueryInfoResponse) *sidecarv1.QueryInfoResponse {
	if info == nil {
		return nil
	}

	out := &sidecarv1.QueryInfoResponse{
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
