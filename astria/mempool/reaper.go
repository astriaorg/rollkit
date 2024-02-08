package mempool

import (
	"fmt"
	"sync"

	"github.com/astriaorg/rollkit/astria/sequencer"
	"github.com/astriaorg/rollkit/mempool"
	"github.com/cometbft/cometbft/libs/log"
)

type MempoolReaper struct {
	c       *sequencer.Client
	mempool *mempool.CListMempool
	logger  log.Logger

	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
}

func NewMempoolReaper(client *sequencer.Client, mempool *mempool.CListMempool, logger log.Logger) *MempoolReaper {
	return &MempoolReaper{
		c:       client,
		mempool: mempool,
		logger:  logger,
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
					mempoolTx := tx0.Value.(*mempool.MempoolTx)

					mr.logger.Info("reaped tx from mempool", "tx", mempoolTx.Tx())

					// submit to shared sequencer
					res, err := mr.c.BroadcastTx(mempoolTx.Tx())
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
