// Package daemon implements the Nova gRPC daemon server.
// All VM lifecycle, network chaos, snapshot, and SSH exec operations
// are exposed over a Unix domain socket.
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/3clabs/nova/internal/config"
	"github.com/3clabs/nova/internal/network"
	"github.com/3clabs/nova/internal/snapshot"
	"github.com/3clabs/nova/internal/state"
	"github.com/3clabs/nova/internal/vm"
	pb "github.com/3clabs/nova/pkg/novapb/nova/v1"
)

// Server implements the nova.v1.NovaServer gRPC service.
type Server struct {
	pb.UnimplementedNovaServer
	orch        *vm.Orchestrator
	conditioner *network.Conditioner
	snapMgr     *snapshot.Manager
	stateDir    string
	shutdownFn  func() // called by the Shutdown RPC
}

// NewServer creates a daemon Server backed by the given state directory.
func NewServer(stateDir string, shutdownFn func()) (*Server, error) {
	orch, err := vm.NewOrchestratorWithDir(stateDir)
	if err != nil {
		return nil, fmt.Errorf("creating orchestrator: %w", err)
	}

	store, err := state.NewStore(stateDir)
	if err != nil {
		return nil, fmt.Errorf("creating state store: %w", err)
	}

	snapMgr, err := snapshot.NewManager(store, stateDir)
	if err != nil {
		return nil, fmt.Errorf("creating snapshot manager: %w", err)
	}

	return &Server{
		orch:        orch,
		conditioner: network.NewConditioner(),
		snapMgr:     snapMgr,
		stateDir:    stateDir,
		shutdownFn:  shutdownFn,
	}, nil
}

// --- Cluster Lifecycle ---

func (s *Server) Apply(ctx context.Context, req *pb.ApplyRequest) (*pb.ApplyResponse, error) {
	if req.HclConfig == "" {
		return nil, status.Error(codes.InvalidArgument, "hcl_config is required")
	}

	// Write HCL to a temp file so the orchestrator can load it.
	cfgPath := filepath.Join(s.stateDir, "daemon-nova.hcl")
	if err := os.WriteFile(cfgPath, []byte(req.HclConfig), 0644); err != nil {
		return nil, status.Errorf(codes.Internal, "writing config: %v", err)
	}

	// Write cloud-config if provided.
	if req.CloudConfigPath != "" {
		data, err := os.ReadFile(req.CloudConfigPath)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "reading cloud-config: %v", err)
		}
		ccPath := filepath.Join(s.stateDir, "cloud-config.yaml")
		os.WriteFile(ccPath, data, 0644)
	}

	if err := s.orch.Up(ctx, cfgPath); err != nil {
		return nil, status.Errorf(codes.Internal, "apply: %v", err)
	}

	// Build response from resolved config + live state.
	cfg, _ := config.Load(cfgPath)
	nodes := cfg.ResolveNodes()

	resp := &pb.ApplyResponse{}
	for _, n := range nodes {
		ip, _ := s.orch.GuestIP(n.Name)
		info := &pb.NodeInfo{
			Name:         n.Name,
			Ip:           ip,
			State:        "running",
			PortForwards: make(map[int32]int32),
		}
		for _, pf := range n.PortForwards {
			info.PortForwards[int32(pf.Host)] = int32(pf.Guest)
		}
		resp.Nodes = append(resp.Nodes, info)
	}

	return resp, nil
}

func (s *Server) Destroy(ctx context.Context, req *pb.DestroyRequest) (*emptypb.Empty, error) {
	if req.Name == "" {
		if err := s.orch.DestroyAll(); err != nil {
			return nil, status.Errorf(codes.Internal, "destroy all: %v", err)
		}
	} else {
		if err := s.orch.Destroy(req.Name); err != nil {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
	}
	return &emptypb.Empty{}, nil
}

// --- Node Control ---

func (s *Server) NodeStop(ctx context.Context, req *pb.NodeRequest) (*emptypb.Empty, error) {
	if err := s.orch.Down(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) NodeStart(ctx context.Context, req *pb.NodeRequest) (*emptypb.Empty, error) {
	// TODO: implement restart of a stopped node (requires re-reading config).
	return nil, status.Error(codes.Unimplemented, "NodeStart not yet implemented")
}

func (s *Server) NodeKill(ctx context.Context, req *pb.NodeRequest) (*emptypb.Empty, error) {
	if err := s.orch.ForceKill(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) NodeStatus(ctx context.Context, req *pb.NodeRequest) (*pb.NodeStatusResponse, error) {
	machines, err := s.orch.Status()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	for _, m := range machines {
		if m.Name == req.Name || m.ID == req.Name {
			ip, _ := s.orch.GuestIP(m.ID)
			return &pb.NodeStatusResponse{
				Name:      m.Name,
				State:     string(m.State),
				Ip:        ip,
				StartedAt: timestamppb.New(m.CreatedAt),
			}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "node %q not found", req.Name)
}

func (s *Server) Status(ctx context.Context, _ *emptypb.Empty) (*pb.StatusResponse, error) {
	machines, err := s.orch.Status()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	resp := &pb.StatusResponse{}
	for _, m := range machines {
		ip, _ := s.orch.GuestIP(m.ID)
		resp.Nodes = append(resp.Nodes, &pb.NodeStatusResponse{
			Name:      m.Name,
			State:     string(m.State),
			Ip:        ip,
			StartedAt: timestamppb.New(m.CreatedAt),
		})
	}
	return resp, nil
}

// --- Network Chaos ---

func (s *Server) LinkDegrade(ctx context.Context, req *pb.LinkDegradeRequest) (*emptypb.Empty, error) {
	latency := req.Latency.AsDuration()
	jitter := req.Jitter.AsDuration()
	if err := s.conditioner.Degrade(req.NodeA, req.NodeB, latency, jitter, req.Loss); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) LinkPartition(ctx context.Context, req *pb.LinkPairRequest) (*emptypb.Empty, error) {
	s.conditioner.Partition(req.NodeA, req.NodeB)
	return &emptypb.Empty{}, nil
}

func (s *Server) LinkHeal(ctx context.Context, req *pb.LinkPairRequest) (*emptypb.Empty, error) {
	s.conditioner.Heal(req.NodeA, req.NodeB)
	return &emptypb.Empty{}, nil
}

func (s *Server) LinkReset(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.conditioner.Reset()
	return &emptypb.Empty{}, nil
}

func (s *Server) LinkStatus(ctx context.Context, _ *emptypb.Empty) (*pb.LinkStatusResponse, error) {
	rules := s.conditioner.AllRules()
	resp := &pb.LinkStatusResponse{}
	for _, r := range rules {
		resp.Conditions = append(resp.Conditions, &pb.LinkCondition{
			NodeA:       r.NodeA,
			NodeB:       r.NodeB,
			Latency:     durationpb.New(r.Latency),
			Jitter:      durationpb.New(r.Jitter),
			Loss:        r.Loss,
			Partitioned: r.Down,
		})
	}
	return resp, nil
}

// --- Snapshots ---

func (s *Server) SnapshotSave(ctx context.Context, req *pb.SnapshotRequest) (*emptypb.Empty, error) {
	if err := s.snapMgr.Save(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) SnapshotRestore(ctx context.Context, req *pb.SnapshotRequest) (*emptypb.Empty, error) {
	if err := s.snapMgr.Restore(req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) SnapshotList(ctx context.Context, _ *emptypb.Empty) (*pb.SnapshotListResponse, error) {
	snaps, err := s.snapMgr.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	resp := &pb.SnapshotListResponse{}
	for _, snap := range snaps {
		resp.Snapshots = append(resp.Snapshots, &pb.SnapshotInfo{
			Name:         snap.Name,
			MachineCount: int32(len(snap.Machines)),
			CreatedAt:    timestamppb.New(snap.CreatedAt),
		})
	}
	return resp, nil
}

func (s *Server) SnapshotDelete(ctx context.Context, req *pb.SnapshotRequest) (*emptypb.Empty, error) {
	if err := s.snapMgr.Delete(req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

// --- SSH Exec ---

func (s *Server) Exec(ctx context.Context, req *pb.ExecRequest) (*pb.ExecResponse, error) {
	timeout := 30 * time.Second
	if req.Timeout != nil {
		timeout = req.Timeout.AsDuration()
	}

	result, err := s.orch.ExecSSH(req.Node, req.Command, timeout)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &pb.ExecResponse{
		ExitCode: int32(result.ExitCode),
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
	}, nil
}

func (s *Server) WaitReady(ctx context.Context, req *pb.WaitReadyRequest) (*emptypb.Empty, error) {
	timeout := 120 * time.Second
	if req.Timeout != nil {
		timeout = req.Timeout.AsDuration()
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := s.orch.WaitReady(ctx, req.Node); err != nil {
		return nil, status.Errorf(codes.DeadlineExceeded, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

// --- Streaming (stubs for now) ---

func (s *Server) StreamLogs(req *pb.NodeRequest, stream pb.Nova_StreamLogsServer) error {
	return status.Error(codes.Unimplemented, "StreamLogs not yet implemented")
}

func (s *Server) StreamEvents(_ *emptypb.Empty, stream pb.Nova_StreamEventsServer) error {
	return status.Error(codes.Unimplemented, "StreamEvents not yet implemented")
}

// --- Daemon Control ---

func (s *Server) Shutdown(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	// Destroy all VMs first.
	s.orch.DestroyAll()
	// Signal the daemon to exit.
	if s.shutdownFn != nil {
		go s.shutdownFn()
	}
	return &emptypb.Empty{}, nil
}
