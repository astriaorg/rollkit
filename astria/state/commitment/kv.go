package commitment

import (
	"context"
	"fmt"
	"time"

	astriaPb "buf.build/gen/go/astria/execution-apis/protocolbuffers/go/astria/execution/v1alpha2"
	ds "github.com/ipfs/go-datastore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const CommitmentKey = "commitment"

type CommitmentState struct {
	store ds.TxnDatastore
	ctx   context.Context
}

func NewCommitmentState(ctx context.Context, store ds.TxnDatastore) *CommitmentState {
	return &CommitmentState{
		store: store,
		ctx:   ctx,
	}
}

func (cs *CommitmentState) UpdateCommitmentState(commitment *astriaPb.CommitmentState) error {
	txn, err := cs.store.NewTransaction(cs.ctx, false)
	if err != nil {
		return fmt.Errorf("failed to create a new batch for transaction: %w", err)
	}
	defer txn.Discard(cs.ctx)

	key := ds.NewKey(CommitmentKey)
	val, err := proto.Marshal(commitment)
	if err != nil {
		return fmt.Errorf("failed to marshal the commitment: %w", err)
	}

	if err := txn.Put(cs.ctx, key, val); err != nil {
		return err
	}

	return txn.Commit(cs.ctx)
}

func (cs *CommitmentState) GetCommitmentState() (*astriaPb.CommitmentState, error) {
	val, err := cs.store.Get(cs.ctx, ds.NewKey(CommitmentKey))
	if err != nil {
		if err == ds.ErrNotFound {
			genHash := [32]byte{0x0}
			pbGenBlock := &astriaPb.Block{
				Number:          uint32(0),
				Hash:            genHash[:],
				ParentBlockHash: genHash[:],
				Timestamp:       timestamppb.New(time.Now()),
			}
			return &astriaPb.CommitmentState{
				Soft: pbGenBlock,
				Firm: pbGenBlock,
			}, nil
		} else {
			return nil, fmt.Errorf("failed to get the commitment: %w", err)
		}
	}

	commitment := &astriaPb.CommitmentState{}
	if err := proto.Unmarshal(val, commitment); err != nil {
		return nil, fmt.Errorf("failed to unmarshal the commitment: %w", err)
	}

	return commitment, nil
}
