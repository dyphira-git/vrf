package sidecar

import (
	"context"

	"google.golang.org/grpc"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
)

type Client interface {
	Randomness(ctx context.Context, in *sidecarv1.QueryRandomnessRequest, opts ...grpc.CallOption) (*sidecarv1.QueryRandomnessResponse, error)
	Info(ctx context.Context, in *sidecarv1.QueryInfoRequest, opts ...grpc.CallOption) (*sidecarv1.QueryInfoResponse, error)

	Start(context.Context) error
	Stop() error
}

type NoOpClient struct{}

func (NoOpClient) Start(context.Context) error {
	return nil
}

func (NoOpClient) Stop() error {
	return nil
}

func (NoOpClient) Randomness(
	_ context.Context,
	_ *sidecarv1.QueryRandomnessRequest,
	_ ...grpc.CallOption,
) (*sidecarv1.QueryRandomnessResponse, error) {
	return nil, nil
}

func (NoOpClient) Info(
	_ context.Context,
	_ *sidecarv1.QueryInfoRequest,
	_ ...grpc.CallOption,
) (*sidecarv1.QueryInfoResponse, error) {
	return nil, nil
}
