package block

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/proxy"
	cmtypes "github.com/cometbft/cometbft/types"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/astriaorg/rollkit/config"
	"github.com/astriaorg/rollkit/mempool"
	"github.com/astriaorg/rollkit/state"
	"github.com/astriaorg/rollkit/store"
	"github.com/astriaorg/rollkit/third_party/log"
	"github.com/astriaorg/rollkit/types"
)

// Manager is responsible for aggregating transactions into blocks.
type SSManager struct {
	lastState    types.State
	lastStateMtx *sync.RWMutex
	store        store.Store
	conf         config.BlockManagerConfig
	genesis      *cmtypes.GenesisDoc
	executor     *state.BlockExecutor
	logger       log.Logger
	proxyApp     proxy.AppConns

	// Rollkit doesn't have "validators", but
	// we store the sequencer in this struct for compatibility.
	validatorSet *cmtypes.ValidatorSet
	proposerKey  cmcrypto.PrivKey

	// for reporting metrics
	metrics *Metrics
}

// NewManager creates new block Manager.
func NewSSManager(
	conf config.BlockManagerConfig,
	genesis *cmtypes.GenesisDoc,
	store store.Store,
	mempool mempool.Mempool,
	proxyApp proxy.AppConns,
	eventBus *cmtypes.EventBus,
	logger log.Logger,
	seqMetrics *Metrics,
	execMetrics *state.Metrics,
) (*SSManager, error) {
	s, err := getInitialState(store, genesis)
	if err != nil {
		return nil, err
	}

	nullKey := ed25519.GenPrivKeyFromSecret([]byte{0x00})
	proposer := nullKey.PubKey().Address()

	exec := state.NewBlockExecutor(proposer, genesis.ChainID, mempool, proxyApp.Consensus(), eventBus, logger, execMetrics)
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

	nullValidator := cmtypes.NewValidator(nullKey.PubKey(), 1)

	agg := &SSManager{
		conf:         conf,
		genesis:      genesis,
		lastState:    s,
		store:        store,
		executor:     exec,
		lastStateMtx: new(sync.RWMutex),
		logger:       logger,
		validatorSet: cmtypes.NewValidatorSet([]*cmtypes.Validator{nullValidator}),
		proposerKey:  nullKey,
		metrics:      seqMetrics,
		proxyApp:     proxyApp,
	}
	return agg, nil
}

func (m *SSManager) CheckCrashRecovery(ctx context.Context) error {
	m.logger.Info("checking for crash recovery")
	res, err := m.proxyApp.Query().Info(ctx, proxy.RequestInfo)
	if err != nil {
		return fmt.Errorf("error calling proxyApp.Query().Info: %v", err)
	}
	m.logger.Info("app handshake", "LastBlockHeight", res.LastBlockHeight, "LastBlockAppHash", res.LastBlockAppHash)
	storeHeight := m.GetStoreHeight()
	if storeHeight != uint64(res.LastBlockHeight) {
		m.logger.Info("store height and app height mismatch", "store_height", storeHeight, "app_height", res.LastBlockHeight)
		if res.LastBlockHeight-int64(storeHeight) == 1 {
			m.logger.Info("committing block", "height", res.LastBlockHeight)
			m.Commit(ctx, uint64(res.LastBlockHeight), true)
		} else {
			panic("what do")
		}
	}
	return nil
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

func (m *SSManager) GetLastState() types.State {
	return m.lastState
}

func (m *SSManager) GetDAHeight() uint64 {
	return m.lastState.DAHeight
}

func (m *SSManager) SetDAHeight(ctx context.Context, height uint64) error {
	m.lastStateMtx.Lock()
	defer m.lastStateMtx.Unlock()
	m.lastState.DAHeight = height
	err := m.store.UpdateState(ctx, m.lastState)
	if err != nil {
		return err
	}
	return nil
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

func (m *SSManager) ExecuteBlock(ctx context.Context, prevBlockHash types.Hash, timestamp time.Time, txs types.Txs) (*types.Block, error) {
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
			return nil, fmt.Errorf("block can only be created on top of soft block. Last recorded block height: %d, hash: %s", height, lastBlock.Hash())
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

	if pendingBlock == nil {
		isAppValid, err := m.executor.ProcessProposal(block, m.lastState)
		if err != nil {
			return nil, err
		}
		if !isAppValid {
			return nil, fmt.Errorf("error while processing the proposal: %v", err)
		}

		// SaveBlock commits the DB tx
		err = m.store.SaveBlock(ctx, block, commit)
		if err != nil {
			return nil, fmt.Errorf("error while saving block: %w", err)
		}
	}

	responses, err := m.applyBlock(ctx, block)
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

	// SaveBlock commits the DB tx
	err = m.store.SaveBlock(ctx, block, commit)
	if err != nil {
		return nil, fmt.Errorf("error while saving block: %w", err)
	}

	// SaveBlockResponses commits the DB tx
	err = m.store.SaveBlockResponses(ctx, blockHeight, responses)
	if err != nil {
		return nil, fmt.Errorf("error while saving block responses: %w", err)
	}

	return block, nil
}

func (m *SSManager) Commit(ctx context.Context, height uint64, skipExec bool) error {
	currHeight := m.store.Height()
	if height != currHeight+1 {
		m.logger.Error("Trying to commit invalid height", "currHeight", currHeight, "newHeight", height)
		return fmt.Errorf("cannot commit an invalid height: current height %d, new height %d", currHeight, height)
	}

	pendingBlock, err := m.store.GetBlock(ctx, height)
	if err != nil {
		return fmt.Errorf("error while loading block: %w", err)
	}

	blockResponses, err := m.store.GetBlockResponses(ctx, height)
	if err != nil {
		return err
	}

	newState, err := m.executor.UpdateState(m.lastState, pendingBlock, blockResponses)
	if err != nil {
		return err
	}

	// Update the stored height
	m.store.SetHeight(ctx, height)

	// Commit the new state and block which writes to disk on the proxy app
	_, err = m.executor.Commit(ctx, newState, pendingBlock, blockResponses, skipExec)
	if err != nil {
		return fmt.Errorf("error while committing block: %w", err)
	}

	// After this call m.lastState is the NEW state returned from ApplyBlock
	// updateState also commits the DB tx
	err = m.updateState(ctx, newState)
	if err != nil {
		return fmt.Errorf("error while updating state: %w", err)
	}

	m.recordMetrics(pendingBlock)

	m.logger.Info("Successfully commited height", "height", height)

	return nil
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

func (m *SSManager) applyBlock(ctx context.Context, block *types.Block) (*abci.ResponseFinalizeBlock, error) {
	m.lastStateMtx.RLock()
	defer m.lastStateMtx.RUnlock()
	return m.executor.ApplyBlock(ctx, m.lastState, block)
}

// getInitialState tries to load lastState from Store, and if it's not available it reads GenesisDoc.
func getInitialState(store store.Store, genesis *cmtypes.GenesisDoc) (types.State, error) {
	// Load the state from store.
	s, err := store.GetState(context.Background())
	if errors.Is(err, ds.ErrNotFound) {
		// If the user is starting a fresh chain (or hard-forking), we assume the stored state is empty.
		s, err = types.NewFromGenesisDoc(genesis)
		if err != nil {
			return types.State{}, err
		}
		store.SetHeight(context.Background(), s.LastBlockHeight)
	} else if err != nil {
		return types.State{}, err
	} else {
		// Perform a sanity-check to stop the user from
		// using a higher genesis than the last stored state.
		// if they meant to hard-fork, they should have cleared the stored State
		if uint64(genesis.InitialHeight) > s.LastBlockHeight {
			return types.State{}, fmt.Errorf("genesis.InitialHeight (%d) is greater than last stored state's LastBlockHeight (%d)", genesis.InitialHeight, s.LastBlockHeight)
		}
	}

	return s, nil
}

func updateState(s *types.State, res *abci.ResponseInitChain) {
	// If the app did not return an app hash, we keep the one set from the genesis doc in
	// the state. We don't set appHash since we don't want the genesis doc app hash
	// recorded in the genesis block. We should probably just remove GenesisDoc.AppHash.
	if len(res.AppHash) > 0 {
		s.AppHash = res.AppHash
	}

	if res.ConsensusParams != nil {
		params := res.ConsensusParams
		if params.Block != nil {
			s.ConsensusParams.Block.MaxBytes = params.Block.MaxBytes
			s.ConsensusParams.Block.MaxGas = params.Block.MaxGas
		}
		if params.Evidence != nil {
			s.ConsensusParams.Evidence.MaxAgeNumBlocks = params.Evidence.MaxAgeNumBlocks
			s.ConsensusParams.Evidence.MaxAgeDuration = params.Evidence.MaxAgeDuration
			s.ConsensusParams.Evidence.MaxBytes = params.Evidence.MaxBytes
		}
		if params.Validator != nil {
			// Copy params.Validator.PubkeyTypes, and set result's value to the copy.
			// This avoids having to initialize the slice to 0 values, and then write to it again.
			s.ConsensusParams.Validator.PubKeyTypes = append([]string{}, params.Validator.PubKeyTypes...)
		}
		if params.Version != nil {
			s.ConsensusParams.Version.App = params.Version.App
		}
		s.Version.Consensus.App = s.ConsensusParams.Version.App
	}
	// We update the last results hash with the empty hash, to conform with RFC-6962.
	s.LastResultsHash = merkle.HashFromByteSlices(nil)

}

func getAddress(key crypto.PrivKey) ([]byte, error) {
	rawKey, err := key.GetPublic().Raw()
	if err != nil {
		return nil, err
	}
	return cmcrypto.AddressHash(rawKey), nil
}
