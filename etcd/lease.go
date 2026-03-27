package etcd

import (
	"context"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
)

// LeaseGrant issues a lease. Strata does not currently enforce lease expiry,
// so this returns the requested TTL as both the ID and TTL.
func (s *Server) LeaseGrant(_ context.Context, r *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error) {
	id := r.ID
	if id == 0 {
		id = r.TTL
	}
	return &etcdserverpb.LeaseGrantResponse{
		Header: s.header(),
		ID:     id,
		TTL:    r.TTL,
	}, nil
}

func (s *Server) LeaseRevoke(_ context.Context, _ *etcdserverpb.LeaseRevokeRequest) (*etcdserverpb.LeaseRevokeResponse, error) {
	return &etcdserverpb.LeaseRevokeResponse{Header: s.header()}, nil
}

func (s *Server) LeaseKeepAlive(stream etcdserverpb.Lease_LeaseKeepAliveServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(&etcdserverpb.LeaseKeepAliveResponse{
			Header: s.header(),
			ID:     req.ID,
			TTL:    60,
		}); err != nil {
			return nil
		}
	}
}

func (s *Server) LeaseTimeToLive(_ context.Context, r *etcdserverpb.LeaseTimeToLiveRequest) (*etcdserverpb.LeaseTimeToLiveResponse, error) {
	return &etcdserverpb.LeaseTimeToLiveResponse{
		Header: s.header(),
		ID:     r.ID,
		TTL:    60,
	}, nil
}

func (s *Server) LeaseLeases(_ context.Context, _ *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error) {
	return &etcdserverpb.LeaseLeasesResponse{Header: s.header()}, nil
}
