package mempool

import (
	"fmt"
	"sync"

	"github.com/rollkit/rollkit/mempool"
	"github.com/rollkit/rollkit/types"

	"github.com/rollkit/rollkit/astria/sequencer"
)

type MempoolReaper struct {
	c       *sequencer.Client
	mempool *mempool.CListMempool

	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
}

func NewMempoolReaper(client *sequencer.Client, mempool *mempool.CListMempool) *MempoolReaper {
	return &MempoolReaper{
		c:       client,
		mempool: mempool,
		started: false,
		stopCh:  make(chan struct{}),
	}
}

// reap tx from the mempool as they occur
func (mr *MempoolReaper) Reap() {
	for {
		select {
		case <-mr.stopCh:
			return
		default:
			// wait for tx to be in mempool
			ch := mr.mempool.TxsWaitChan()
			<-ch

			// get first tx in pool
			tx0 := mr.mempool.TxsFront()
		TxNext:
			for {
				select {
				case <-mr.stopCh:
					return
				default:
					mempoolTx := tx0.Value.(*mempoolTx)

					// submit to shared sequencer
					res, err := mr.c.BroadcastTx(mempoolTx.tx)
					if err != nil {
						panic(fmt.Sprintf("error sending message: %s\n", err))
					}
					println(res.Log)

					// wait for next tx
					tx0 = tx0.NextWait()

					// tx was last element and was removed (pool is empty?)
					if tx0 == nil {
						break TxNext
					}
				}
			}
		}
	}
}

func (mr *MempoolReaper) Start() error {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	// Ensure Reap is only run once
	if mr.started {
		return nil
	}

	go mr.Reap()
	mr.started = true
	return nil
}

func (mr *MempoolReaper) Stop() error {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if !mr.started {
		return nil
	}

	close(mr.stopCh)
	mr.started = false
	return nil
}

// copied from rollkit clist_mempool.go
//--------------------------------------------------------------------------------

// mempoolTx is a transaction that successfully ran
type mempoolTx struct {
	height    uint64   // height that this tx had been validated in
	gasWanted int64    // amount of gas this tx states it will require
	tx        types.Tx //

	// ids of peers who've sent us this tx (as a map for quick lookups).
	// senders: PeerID -> bool
	senders sync.Map
}
