package execution

import (
	log "log/slog"
	"net"
	"sync"

	astriaGrpc "buf.build/gen/go/astria/execution-apis/grpc/go/astria/execution/v1alpha2/executionv1alpha2grpc"
	"google.golang.org/grpc"
)

// GRPCServerHandler is the gRPC server handler.
// It gives us a way to attach the gRPC server to the node so it can be stopped on shutdown.
type GRPCServerHandler struct {
	mu sync.Mutex

	endpoint                   string
	server                     *grpc.Server
	executionServiceServerV1a2 *astriaGrpc.ExecutionServiceServer
}

// NewServer creates a new gRPC server.
// It registers the execution service server.
// It registers the gRPC server with the node so it can be stopped on shutdown.
func NewGRPCServerHandler(execServ astriaGrpc.ExecutionServiceServer, endpoint string) *GRPCServerHandler {
	server := grpc.NewServer()

	log.Info("gRPC server enabled", "endpoint", endpoint)

	handler := &GRPCServerHandler{
		endpoint:                   endpoint,
		server:                     server,
		executionServiceServerV1a2: &execServ,
	}

	astriaGrpc.RegisterExecutionServiceServer(server, execServ)
	return handler
}

// Start starts the gRPC server if it is enabled.
func (handler *GRPCServerHandler) Start() error {
	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.endpoint == "" {
		return nil
	}

	// Start the gRPC server
	lis, err := net.Listen("tcp", handler.endpoint)
	if err != nil {
		return err
	}
	go handler.server.Serve(lis)
	log.Info("gRPC server started", "endpoint", handler.endpoint)
	return nil
}

// Stop stops the gRPC server.
func (handler *GRPCServerHandler) Stop() error {
	handler.mu.Lock()
	defer handler.mu.Unlock()

	handler.server.GracefulStop()
	log.Info("gRPC server stopped", "endpoint", handler.endpoint)
	return nil
}
