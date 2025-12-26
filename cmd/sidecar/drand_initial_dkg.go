package main

// InitialDKGRole indicates whether the local sidecar is acting as the DKG leader
// or as a joiner that receives the proposal from the leader.
type InitialDKGRole string

const (
	InitialDKGRoleLeader InitialDKGRole = "leader"
	InitialDKGRoleJoiner InitialDKGRole = "joiner"
)
