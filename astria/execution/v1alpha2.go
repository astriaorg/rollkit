package execution

import (
	"context"
	"sync"
	"time"

	astriaGrpc "buf.build/gen/go/astria/execution-apis/grpc/go/astria/execution/v1alpha2/executionv1alpha2grpc"
	astriaPb "buf.build/gen/go/astria/execution-apis/protocolbuffers/go/astria/execution/v1alpha2"
	cmbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/rollkit/rollkit/block"
	"github.com/rollkit/rollkit/store"
	"github.com/rollkit/rollkit/types"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ExecutionServiceServerV1Alpha2 struct {
	// NOTE - from the generated code: All implementations must embed
	// UnimplementedExecutionServiceServer for forward compatibility
	astriaGrpc.UnimplementedExecutionServiceServer

	store              store.Store
	blockManager       *block.SSManager
	genesis            GenesisInfo
	logger             log.Logger
	blockExecutionLock sync.Mutex
}

type GenesisInfo struct {
	RollupId                    [32]byte
	SequencerGenesisBlockHeight uint64
	CelestiaBaseBlockHeight     uint64
	CelestiaBlockVariance       uint64
}

func NewExecutionServiceServerV1Alpha2(blockManager *block.SSManager, genesis GenesisInfo, store store.Store, logger log.Logger) *ExecutionServiceServerV1Alpha2 {
	return &ExecutionServiceServerV1Alpha2{
		blockManager: blockManager,
		genesis:      genesis,
		store:        store,
		logger:       logger,
	}
}

func (s *ExecutionServiceServerV1Alpha2) GetGenesisInfo(ctx context.Context, req *astriaPb.GetGenesisInfoRequest) (*astriaPb.GenesisInfo, error) {
	return &astriaPb.GenesisInfo{
		RollupId:                    s.genesis.RollupId[:],
		SequencerGenesisBlockHeight: uint32(s.genesis.SequencerGenesisBlockHeight),
		CelestiaBaseBlockHeight:     uint32(s.genesis.CelestiaBaseBlockHeight),
		CelestiaBlockVariance:       uint32(s.genesis.CelestiaBlockVariance),
	}, nil
}

// GetBlock retrieves a block by its identifier.
func (s *ExecutionServiceServerV1Alpha2) GetBlock(ctx context.Context, req *astriaPb.GetBlockRequest) (*astriaPb.Block, error) {
	reqJson, _ := protojson.Marshal(req)
	s.logger.Info("GetBlock called", "request", reqJson)

	res, err := s.getBlockFromIdentifier(ctx, req.GetIdentifier())
	if err != nil {
		s.logger.Error("failed finding block", err)
		return nil, err
	}

	resJson, _ := protojson.Marshal(res)
	s.logger.Info("GetBlock completed", "response", resJson)
	return res, nil
}

// BatchGetBlocks will return an array of Blocks given an array of block identifiers.
func (s *ExecutionServiceServerV1Alpha2) BatchGetBlocks(ctx context.Context, req *astriaPb.BatchGetBlocksRequest) (*astriaPb.BatchGetBlocksResponse, error) {
	reqJson, _ := protojson.Marshal(req)
	s.logger.Info("BatchGetBlocks called", "request", reqJson)

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

	resJson, _ := protojson.Marshal(res)
	s.logger.Info("BatchGetBlocks completed", "response", resJson)
	return res, nil
}

// ExecuteBlock drives deterministic derivation of a rollup block from sequencer block data
func (s *ExecutionServiceServerV1Alpha2) ExecuteBlock(ctx context.Context, req *astriaPb.ExecuteBlockRequest) (*astriaPb.Block, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	reqJson, _ := protojson.Marshal(req)
	s.logger.Info("ExecuteBlock called", "request", reqJson)

	s.blockExecutionLock.Lock()
	defer s.blockExecutionLock.Unlock()

	txs := make(types.Txs, len(req.Transactions))
	for i := range txs {
		txs[i] = types.Tx(req.Transactions[i])
	}

	block, err := s.blockManager.PublishBlock(ctx, types.Hash(req.PrevBlockHash), req.Timestamp.AsTime(), txs)
	if err != nil {
		s.logger.Error("Failed to publish block to chain", "hash", block.Hash(), "prevHash", types.Hash(req.PrevBlockHash), "err", err)
		return nil, status.Error(codes.Internal, "failed to insert block to chain")
	}

	s.logger.Info("Published block", "height", block.Height(), "timestamp", block.Time(), "hash", block.Hash(), "parent_hash", block.LastHeader())

	parentHash := block.LastHeader()
	if block.Height() == 1 {
		zeroHash := [32]byte{0x0}
		parentHash = types.Hash(zeroHash[:])
	}

	res := &astriaPb.Block{
		Number:          uint32(block.Height()),
		Hash:            cmbytes.HexBytes(block.Hash()),
		ParentBlockHash: cmbytes.HexBytes(parentHash),
		Timestamp:       timestamppb.New(block.Time()),
	}

	resJson, _ := protojson.Marshal(res)
	s.logger.Info("ExecuteBlock completed", "response", resJson)
	return res, nil
}

// GetCommitmentState fetches the current CommitmentState of the chain.
func (s *ExecutionServiceServerV1Alpha2) GetCommitmentState(ctx context.Context, req *astriaPb.GetCommitmentStateRequest) (*astriaPb.CommitmentState, error) {
	reqJson, _ := protojson.Marshal(req)
	s.logger.Info("GetCommitmentState called", "request", reqJson)

	var res *astriaPb.CommitmentState

	height := s.blockManager.GetStoreHeight()

	if height == 0 {
		genHash := [32]byte{0x0}
		pbGenBlock := &astriaPb.Block{
			Number:          uint32(0),
			Hash:            genHash[:],
			ParentBlockHash: genHash[:],
			Timestamp:       timestamppb.New(time.Now()),
		}
		res = &astriaPb.CommitmentState{
			Soft: pbGenBlock,
			Firm: pbGenBlock,
		}
	} else {
		block, err := s.store.GetBlock(ctx, height)
		if err != nil {
			s.logger.Error("failed finding block with height", "height", height, "error", err)
			return nil, err
		}

		pbBlock := &astriaPb.Block{
			Number:          uint32(block.Height()),
			Hash:            cmbytes.HexBytes(block.Hash()),
			ParentBlockHash: cmbytes.HexBytes(block.LastHeader()),
			Timestamp:       timestamppb.New(block.Time()),
		}

		res = &astriaPb.CommitmentState{
			Soft: pbBlock,
			Firm: pbBlock,
		}
	}

	resJson, _ := protojson.Marshal(res)
	s.logger.Info("GetCommitmentState completed", "response", resJson)
	return res, nil
}

// UpdateCommitmentState replaces the whole CommitmentState with a new CommitmentState.
func (s *ExecutionServiceServerV1Alpha2) UpdateCommitmentState(ctx context.Context, req *astriaPb.UpdateCommitmentStateRequest) (*astriaPb.CommitmentState, error) {
	reqJson, _ := protojson.Marshal(req)
	s.logger.Info("UpdateCommitmentState called", "request", reqJson)
	return req.CommitmentState, nil
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
