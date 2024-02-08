package block

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/proxy"
	cmtypes "github.com/cometbft/cometbft/types"
	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/rollkit/rollkit/config"
	"github.com/rollkit/rollkit/mempool"
	"github.com/rollkit/rollkit/state"
	"github.com/rollkit/rollkit/store"
	"github.com/rollkit/rollkit/third_party/log"
	"github.com/rollkit/rollkit/types"
)

// Manager is responsible for aggregating transactions into blocks.
type SSManager struct {
	lastState    types.State
	lastStateMtx *sync.RWMutex
	store        store.Store
	conf         config.BlockManagerConfig
	genesis      *cmtypes.GenesisDoc
	proposerKey  crypto.PrivKey
	executor     *state.BlockExecutor
	logger       log.Logger

	// Rollkit doesn't have "validators", but
	// we store the sequencer in this struct for compatibility.
	validatorSet *cmtypes.ValidatorSet

	// for reporting metrics
	metrics *Metrics
}

// NewManager creates new block Manager.
func NewSSManager(
	proposerKey crypto.PrivKey,
	conf config.BlockManagerConfig,
	genesis *cmtypes.GenesisDoc,
	store store.Store,
	mempool mempool.Mempool,
	proxyApp proxy.AppConnConsensus,
	eventBus *cmtypes.EventBus,
	logger log.Logger,
	seqMetrics *Metrics,
	execMetrics *state.Metrics,
) (*SSManager, error) {
	s, err := getInitialState(store, genesis)
	if err != nil {
		return nil, err
	}
	// genesis should have exactly one "validator", the centralized sequencer.
	// this should have been validated in the above call to getInitialState.
	valSet := types.GetValidatorSetFromGenesis(genesis)

	proposerAddress, err := getAddress(proposerKey)
	if err != nil {
		return nil, err
	}

	exec := state.NewBlockExecutor(proposerAddress, genesis.ChainID, mempool, proxyApp, eventBus, logger, execMetrics)
	if s.LastBlockHeight+1 == uint64(genesis.InitialHeight) {
		logger.Info("Initializing chain")
		res, err := exec.InitChain(genesis)
		if err != nil {
			return nil, err
		}

		updateState(&s, res)
		if err := store.UpdateState(context.Background(), s); err != nil {
			return nil, err
		}
	}

	agg := &SSManager{
		proposerKey:  proposerKey,
		conf:         conf,
		genesis:      genesis,
		lastState:    s,
		store:        store,
		executor:     exec,
		lastStateMtx: new(sync.RWMutex),
		logger:       logger,
		validatorSet: &valSet,
		metrics:      seqMetrics,
	}
	return agg, nil
}

// SetLastState is used to set lastState used by Manager.
func (m *SSManager) SetLastState(state types.State) {
	m.lastStateMtx.Lock()
	defer m.lastStateMtx.Unlock()
	m.lastState = state
}

// GetStoreHeight returns the manager's store height
func (m *SSManager) GetStoreHeight() uint64 {
	return m.store.Height()
}

func (m *SSManager) getCommit(header types.Header) (*types.Commit, error) {
	headerBytes, err := header.MarshalBinary()
	if err != nil {
		return nil, err
	}
	sign, err := m.proposerKey.Sign(headerBytes)
	if err != nil {
		return nil, err
	}
	return &types.Commit{
		Signatures: []types.Signature{sign},
	}, nil
}

func (m *SSManager) PublishBlock(ctx context.Context, prevBlockHash types.Hash, timestamp time.Time, txs types.Txs) (*types.Block, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var lastCommit *types.Commit
	var lastHeaderHash types.Hash
	height := m.store.Height()
	newHeight := height + 1

	// this is a special case, when first block is produced - there is no previous commit
	if newHeight == uint64(m.genesis.InitialHeight) {
		lastCommit = &types.Commit{}
	} else {
		var err error
		lastCommit, err = m.store.GetCommit(ctx, height)
		if err != nil {
			return nil, fmt.Errorf("error while loading last commit: %w", err)
		}
		lastBlock, err := m.store.GetBlock(ctx, height)
		if err != nil {
			return nil, fmt.Errorf("error while loading last block: %w", err)
		}
		// Validate block being created has valid previous hash
		lastHeaderHash = lastBlock.Hash()
		if !bytes.Equal(lastHeaderHash, prevBlockHash) {
			return nil, fmt.Errorf("block can only be created on top of soft block.")
		}
	}

	var block *types.Block
	var commit *types.Commit

	// Check if there's an already stored block at a newer height
	// If there is use that instead of creating a new block
	pendingBlock, err := m.store.GetBlock(ctx, newHeight)
	if err == nil {
		m.logger.Info("Using pending block", "height", newHeight)
		block = pendingBlock
	} else {
		m.logger.Info("Creating and publishing block", "height", newHeight)
		block, err = m.createBlock(newHeight, timestamp, txs, lastCommit, lastHeaderHash)
		if err != nil {
			return nil, fmt.Errorf("error while creating block: %w", err)
		}
		m.logger.Debug("block info", "num_tx", len(block.Data.Txs))

		/*
		   here we set the SignedHeader.DataHash, and SignedHeader.Commit as a hack
		   to make the block pass ValidateBasic() when it gets called by applyBlock on line 681
		   these values get overridden on lines 687-698 after we obtain the IntermediateStateRoots.
		*/
		block.SignedHeader.DataHash, err = block.Data.Hash()
		if err != nil {
			return nil, fmt.Errorf("error while hashing block data: %w", err)
		}

		commit, err = m.getCommit(block.SignedHeader.Header)
		if err != nil {
			return nil, fmt.Errorf("error while getting commit: %w", err)
		}

		// set the commit to current block's signed header
		block.SignedHeader.Commit = *commit
		err = m.store.SaveBlock(ctx, block, commit)
		if err != nil {
			return nil, fmt.Errorf("error while saving block: %w", err)
		}
	}

	block.SignedHeader.Validators = m.validatorSet

	newState, responses, err := m.applyBlock(ctx, block)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("error while applying block: %w", err)
		}
		// if call to applyBlock fails, we halt the node, see https://github.com/cometbft/cometbft/pull/496
		panic(err)
	}

	// Before taking the hash, we need updated ISRs, hence after ApplyBlock
	block.SignedHeader.Header.DataHash, err = block.Data.Hash()
	if err != nil {
		return nil, fmt.Errorf("error while hashing block header data: %w", err)
	}

	commit, err = m.getCommit(block.SignedHeader.Header)
	if err != nil {
		return nil, fmt.Errorf("error while getting commit: %w", err)
	}

	// set the commit to current block's signed header
	block.SignedHeader.Commit = *commit

	// Validate the created block before storing
	if err := m.executor.Validate(m.lastState, block); err != nil {
		return nil, fmt.Errorf("failed to validate block: %w", err)
	}

	blockHeight := block.Height()
	// Update the stored height before submitting to the DA layer and committing to the DB
	m.store.SetHeight(ctx, blockHeight)

	// SaveBlock commits the DB tx
	err = m.store.SaveBlock(ctx, block, commit)
	if err != nil {
		return nil, fmt.Errorf("error while saving block: %w", err)
	}

	// Commit the new state and block which writes to disk on the proxy app
	_, _, err = m.executor.Commit(ctx, newState, block, responses)
	if err != nil {
		return nil, fmt.Errorf("error while committing block: %w", err)
	}

	// SaveBlockResponses commits the DB tx
	err = m.store.SaveBlockResponses(ctx, blockHeight, responses)
	if err != nil {
		return nil, fmt.Errorf("error while saving block responses: %w", err)
	}

	// After this call m.lastState is the NEW state returned from ApplyBlock
	// updateState also commits the DB tx
	err = m.updateState(ctx, newState)
	if err != nil {
		return nil, fmt.Errorf("error while updating state: %w", err)
	}

	m.recordMetrics(block)

	m.logger.Debug("successfully proposed block", "proposer", hex.EncodeToString(block.SignedHeader.ProposerAddress), "height", blockHeight)

	return block, nil
}

func (m *SSManager) recordMetrics(block *types.Block) {
	m.metrics.NumTxs.Set(float64(len(block.Data.Txs)))
	m.metrics.TotalTxs.Add(float64(len(block.Data.Txs)))
	m.metrics.BlockSizeBytes.Set(float64(block.Size()))
	m.metrics.CommittedHeight.Set(float64(block.Height()))
}

// Updates the state stored in manager's store along the manager's lastState
func (m *SSManager) updateState(ctx context.Context, s types.State) error {
	m.lastStateMtx.Lock()
	defer m.lastStateMtx.Unlock()
	err := m.store.UpdateState(ctx, s)
	if err != nil {
		return err
	}
	m.lastState = s
	m.metrics.Height.Set(float64(s.LastBlockHeight))
	return nil
}

func (m *SSManager) getLastBlockTime() time.Time {
	m.lastStateMtx.RLock()
	defer m.lastStateMtx.RUnlock()
	return m.lastState.LastBlockTime
}

func (m *SSManager) createBlock(height uint64, timestamp time.Time, txs types.Txs, lastCommit *types.Commit, lastHeaderHash types.Hash) (*types.Block, error) {
	m.lastStateMtx.RLock()
	defer m.lastStateMtx.RUnlock()
	return m.executor.CreateBlockFromSeqencer(height, timestamp, txs, lastCommit, lastHeaderHash, m.lastState)
}

func (m *SSManager) applyBlock(ctx context.Context, block *types.Block) (types.State, *abci.ResponseFinalizeBlock, error) {
	m.lastStateMtx.RLock()
	defer m.lastStateMtx.RUnlock()
	return m.executor.ApplyBlock(ctx, m.lastState, block)
}
