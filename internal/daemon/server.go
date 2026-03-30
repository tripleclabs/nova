// Package daemon implements the Nova gRPC daemon server.
// All VM lifecycle, network chaos, snapshot, and SSH exec operations
// are exposed over a Unix domain socket.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tripleclabs/nova/internal/config"
	"github.com/tripleclabs/nova/internal/network"
	"github.com/tripleclabs/nova/internal/snapshot"
	"github.com/tripleclabs/nova/internal/state"
	"github.com/tripleclabs/nova/internal/vm"
	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// Server implements the nova.v1.NovaServer gRPC service.
type Server struct {
	pb.UnimplementedNovaServer
	orch        *vm.Orchestrator
	conditioner *network.Conditioner
	sw          *network.L2Switch
	snapMgr     *snapshot.Manager
	stateDir    string
	shutdownFn  func() // called by the Shutdown RPC
	events      *eventBroadcaster
}

// eventBroadcaster fans out ClusterEvents to all active StreamEvents subscribers.
type eventBroadcaster struct {
	mu   sync.Mutex
	subs map[uint64]chan *pb.ClusterEvent
	next uint64
}

func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subs: make(map[uint64]chan *pb.ClusterEvent)}
}

func (b *eventBroadcaster) subscribe() (uint64, chan *pb.ClusterEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan *pb.ClusterEvent, 128)
	b.subs[id] = ch
	return id, ch
}

func (b *eventBroadcaster) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

func (b *eventBroadcaster) publish(evt *pb.ClusterEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default: // subscriber is slow; drop rather than block the orchestrator
		}
	}
}

// NewServer creates a daemon Server backed by the given state directory.
func NewServer(stateDir string, shutdownFn func()) (*Server, error) {
	cond := network.NewConditioner()

	// Attempt to start the L2 switch (Linux only; stub returns nil on other platforms).
	tapName := tapDeviceName(stateDir)
	sw, err := network.NewL2Switch(cond, tapName)
	if err != nil {
		slog.Warn("L2 switch unavailable (missing CAP_NET_ADMIN?), falling back to SLIRP", "tap", tapName, "err", err)
		sw = nil
	}

	orch, err := vm.NewOrchestratorWithSwitch(stateDir, sw)
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
		conditioner: cond,
		sw:          sw,
		snapMgr:     snapMgr,
		stateDir:    stateDir,
		shutdownFn:  shutdownFn,
		events:      newEventBroadcaster(),
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

	emit := func(node, msg string) {
		s.events.publish(&pb.ClusterEvent{
			Type:      "log",
			Node:      node,
			Detail:    msg,
			Timestamp: timestamppb.Now(),
		})
	}

	// Extract client metadata: config directory for path resolution, and session
	// ID so concurrent nova-up calls can distinguish their own apply_done event.
	configDir := ""
	sessionID := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("nova-config-dir"); len(vals) > 0 {
			configDir = vals[0]
		}
		if vals := md.Get("nova-session-id"); len(vals) > 0 {
			sessionID = vals[0]
		}
	}

	if err := s.orch.Up(ctx, cfgPath, configDir, emit); err != nil {
		return nil, status.Errorf(codes.Internal, "apply: %v", err)
	}

	// Signal streaming clients that apply is complete. The session ID is echoed
	// in Detail so concurrent nova-up calls can ignore each other's done signals.
	s.events.publish(&pb.ClusterEvent{
		Type:      "apply_done",
		Detail:    sessionID,
		Timestamp: timestamppb.Now(),
	})

	// Build response from resolved config + live state.
	cfg, _ := config.Load(cfgPath)
	nodes := cfg.ResolveNodes()

	resp := &pb.ApplyResponse{}
	for _, n := range nodes {
		ip := s.resolveIP(n.Name)
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
			ip := s.resolveIP(m.ID)
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
		ip := s.resolveIP(m.ID)
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

// --- Export ---

func (s *Server) Export(ctx context.Context, req *pb.ExportRequest) (*pb.ExportResponse, error) {
	format, err := vm.ParseExportFormat(req.Format)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	// Check if a user block was configured by reading the shell_user file.
	nodeName := req.Name
	if nodeName == "" {
		nodeName = "default"
	}
	hasUser := false
	if data, err := os.ReadFile(filepath.Join(s.stateDir, "machines", nodeName, "shell_user")); err == nil {
		hasUser = strings.TrimSpace(string(data)) != "nova"
	}

	emit := func(msg string) {
		s.events.publish(&pb.ClusterEvent{
			Type:      "log",
			Node:      nodeName,
			Detail:    msg,
			Timestamp: timestamppb.Now(),
		})
	}

	opts := vm.ExportOptions{
		Format:        format,
		OutputPath:    req.OutputPath,
		NoClean:       req.NoClean,
		ZeroFreeSpace: req.ZeroFreeSpace,
		SnapshotName:  req.SnapshotName,
		HasUser:       hasUser,
		Emit:          emit,
	}

	result, err := s.orch.Export(ctx, req.Name, opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &pb.ExportResponse{
		OutputPath: result.OutputPath,
		Format:     string(result.Format),
		SizeBytes:  result.SizeBytes,
	}, nil
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
	id, ch := s.events.subscribe()
	defer s.events.unsubscribe(id)

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// resolveIP returns the guest IP for a node, reading from ssh_endpoint.json
// first (written during boot with the discovered IP), falling back to GuestIP().
func (s *Server) resolveIP(name string) string {
	// Try the endpoint file first (has the DHCP-discovered IP on macOS).
	data, err := os.ReadFile(filepath.Join(s.stateDir, "machines", name, "ssh_endpoint.json"))
	if err == nil {
		var ep struct {
			Host string `json:"host"`
		}
		if json.Unmarshal(data, &ep) == nil && ep.Host != "" {
			return ep.Host
		}
	}
	// Fallback to live hypervisor query.
	ip, _ := s.orch.GuestIP(name)
	return ip
}

// --- Daemon Control ---

// tapDeviceName derives a unique TAP interface name from the state directory.
// Each daemon instance gets its own device, preventing conflicts when multiple
// daemons run concurrently (e.g., integration test parallelism or a lingering
// daemon from a previous session). Linux interface names are limited to 15 chars;
// "nova-" (5) + 8 hex digits = 13.
func tapDeviceName(stateDir string) string {
	h := fnv.New32a()
	h.Write([]byte(stateDir))
	return fmt.Sprintf("nova-%08x", h.Sum32())
}

func (s *Server) Shutdown(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	// Destroy all VMs first.
	s.orch.DestroyAll()
	// Signal the daemon to exit.
	if s.shutdownFn != nil {
		go s.shutdownFn()
	}
	return &emptypb.Empty{}, nil
}
