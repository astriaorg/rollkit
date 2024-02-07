package execution

import (
	"context"
	"sync"

	astriaGrpc "buf.build/gen/go/astria/astria/grpc/go/astria/execution/v1alpha2/executionv1alpha2grpc"
	astriaPb "buf.build/gen/go/astria/astria/protocolbuffers/go/astria/execution/v1alpha2"
	cmbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/rollkit/rollkit/block"
	"github.com/rollkit/rollkit/store"
	"github.com/rollkit/rollkit/types"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ExecutionServiceServerV1Alpha2 struct {
	// NOTE - from the generated code: All implementations must embed
	// UnimplementedExecutionServiceServer for forward compatibility
	astriaGrpc.UnimplementedExecutionServiceServer

	store              store.Store
	blockManager       *block.SSManager
	logger             log.Logger
	blockExecutionLock sync.Mutex
}

func NewExecutionServiceServerV1Alpha2(blockManager *block.SSManager, store store.Store, logger log.Logger) *ExecutionServiceServerV1Alpha2 {
	return &ExecutionServiceServerV1Alpha2{
		blockManager: blockManager,
		store:        store,
		logger:       logger,
	}
}

// GetBlock retrieves a block by its identifier.
func (s *ExecutionServiceServerV1Alpha2) GetBlock(ctx context.Context, req *astriaPb.GetBlockRequest) (*astriaPb.Block, error) {
	s.logger.Info("GetBlock called", "request", req)

	res, err := s.getBlockFromIdentifier(ctx, req.GetIdentifier())
	if err != nil {
		s.logger.Error("failed finding block", err)
		return nil, err
	}

	s.logger.Info("GetBlock completed", "request", req, "response", res)
	return res, nil
}

// BatchGetBlocks will return an array of Blocks given an array of block identifiers.
func (s *ExecutionServiceServerV1Alpha2) BatchGetBlocks(ctx context.Context, req *astriaPb.BatchGetBlocksRequest) (*astriaPb.BatchGetBlocksResponse, error) {
	s.logger.Info("BatchGetBlocks called", "request", req)
	var blocks []*astriaPb.Block

	ids := req.GetIdentifiers()
	for _, id := range ids {
		block, err := s.getBlockFromIdentifier(ctx, id)
		if err != nil {
			s.logger.Error("failed finding block with id", id, "error", err)
			return nil, err
		}

		blocks = append(blocks, block)
	}

	res := &astriaPb.BatchGetBlocksResponse{
		Blocks: blocks,
	}

	s.logger.Info("BatchGetBlocks completed", "request", req, "response", res)
	return res, nil
}

// ExecuteBlock drives deterministic derivation of a rollup block from sequencer block data
func (s *ExecutionServiceServerV1Alpha2) ExecuteBlock(ctx context.Context, req *astriaPb.ExecuteBlockRequest) (*astriaPb.Block, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.logger.Info("ExecuteBlock called", "request", req)

	s.blockExecutionLock.Lock()
	defer s.blockExecutionLock.Unlock()

	txs := make(types.Txs, len(req.Transactions))
	for i := range txs {
		txs[i] = types.Tx(req.Transactions[i])
	}

	block, err := s.blockManager.PublishBlock(ctx, types.Hash(req.PrevBlockHash), req.Timestamp.AsTime(), txs)
	if err != nil {
		s.logger.Error("failed to publish block to chain", "hash", block.Hash(), "prevHash", req.PrevBlockHash, "err", err)
		return nil, status.Error(codes.Internal, "failed to insert block to chain")
	}

	res := &astriaPb.Block{
		Number:          uint32(block.Height()),
		Hash:            cmbytes.HexBytes(block.Hash()),
		ParentBlockHash: cmbytes.HexBytes(block.LastHeader()),
		Timestamp:       timestamppb.New(block.Time()),
	}

	s.logger.Info("ExecuteBlock completed", "request", req, "response", res)
	return res, nil
}

// GetCommitmentState fetches the current CommitmentState of the chain.
func (s *ExecutionServiceServerV1Alpha2) GetCommitmentState(ctx context.Context, req *astriaPb.GetCommitmentStateRequest) (*astriaPb.CommitmentState, error) {
	s.logger.Info("GetCommitmentState called", "request", req)
	return nil, nil
}

// UpdateCommitmentState replaces the whole CommitmentState with a new CommitmentState.
func (s *ExecutionServiceServerV1Alpha2) UpdateCommitmentState(ctx context.Context, req *astriaPb.UpdateCommitmentStateRequest) (*astriaPb.CommitmentState, error) {
	s.logger.Info("UpdateCommitmentState called", "request", req)
	return nil, nil
}

func (s *ExecutionServiceServerV1Alpha2) getBlockFromIdentifier(ctx context.Context, identifier *astriaPb.BlockIdentifier) (*astriaPb.Block, error) {
	var block *types.Block
	var err error

	switch idType := identifier.Identifier.(type) {
	case *astriaPb.BlockIdentifier_BlockNumber:
		block, err = s.store.GetBlock(ctx, uint64(identifier.GetBlockNumber()))
		if err != nil {
			return nil, err
		}
	case *astriaPb.BlockIdentifier_BlockHash:
		block, err = s.store.GetBlockByHash(ctx, types.Hash(identifier.GetBlockHash()))
		if err != nil {
			return nil, err
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "identifier has unexpected type %T", idType)
	}

	if block == nil {
		return nil, status.Errorf(codes.NotFound, "Couldn't locate block with identifier %s", identifier.Identifier)
	}

	return &astriaPb.Block{
		Number:          uint32(block.Height()),
		Hash:            cmbytes.HexBytes(block.Hash()),
		ParentBlockHash: cmbytes.HexBytes(block.LastHeader()),
		Timestamp:       timestamppb.New(block.Time()),
	}, nil
}
