package main

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
)

type drandControlServer struct {
	sidecarv1.UnimplementedDrandControlServer

	ctl *drandController
}

func newDrandControlServer(ctl *drandController) *drandControlServer {
	return &drandControlServer{ctl: ctl}
}

func (s *drandControlServer) Status(
	_ context.Context,
	_ *sidecarv1.DrandStatusRequest,
) (*sidecarv1.DrandStatusResponse, error) {
	if s.ctl == nil {
		return nil, status.Error(codes.FailedPrecondition, "drand supervision is disabled")
	}

	st := s.ctl.Status()
	return &sidecarv1.DrandStatusResponse{
		Running:      st.Running,
		Pid:          int64(st.PID),
		RestartCount: uint32(st.RestartCount),
	}, nil
}

func (s *drandControlServer) Start(
	ctx context.Context,
	_ *sidecarv1.DrandStartRequest,
) (*sidecarv1.DrandStartResponse, error) {
	if s.ctl == nil {
		return nil, status.Error(codes.FailedPrecondition, "drand supervision is disabled")
	}

	if err := s.ctl.Start(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "starting drand: %v", err)
	}

	st := s.ctl.Status()
	return &sidecarv1.DrandStartResponse{
		Running: st.Running,
		Pid:     int64(st.PID),
	}, nil
}

func (s *drandControlServer) Stop(
	_ context.Context,
	_ *sidecarv1.DrandStopRequest,
) (*sidecarv1.DrandStopResponse, error) {
	if s.ctl == nil {
		return nil, status.Error(codes.FailedPrecondition, "drand supervision is disabled")
	}

	s.ctl.Stop()
	st := s.ctl.Status()
	return &sidecarv1.DrandStopResponse{
		Running: st.Running,
	}, nil
}

func (s *drandControlServer) Logs(
	_ context.Context,
	req *sidecarv1.DrandLogsRequest,
) (*sidecarv1.DrandLogsResponse, error) {
	if s.ctl == nil {
		return nil, status.Error(codes.FailedPrecondition, "drand supervision is disabled")
	}

	tail := int(req.GetTail())
	if tail <= 0 {
		tail = 200
	}
	if tail > 4096 {
		tail = 4096
	}

	entries := s.ctl.TailLogs(tail)
	if len(entries) == 0 {
		st := s.ctl.Status()
		if !st.Running {
			return nil, status.Error(codes.Unavailable, "drand is not running")
		}
	}

	out := make([]*sidecarv1.DrandLogEntry, 0, len(entries))
	for _, e := range entries {
		stream := e.Stream
		if stream == "" {
			stream = "unknown"
		}
		out = append(out, &sidecarv1.DrandLogEntry{
			UnixNano: e.UnixNano,
			Stream:   stream,
			Line:     fmt.Sprintf("%s", e.Line),
		})
	}

	return &sidecarv1.DrandLogsResponse{Entries: out}, nil
}
