package node

import (
	"context"

	"github.com/cometbft/cometbft/libs/log"
	proxy "github.com/cometbft/cometbft/proxy"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	cmtypes "github.com/cometbft/cometbft/types"

	"github.com/astriaorg/rollkit/config"
)

// Node is the interface for a rollup node
type Node interface {
	Start() error
	GetClient() rpcclient.Client
	Stop() error
	IsRunning() bool
	Cancel()
}

// NewNode returns a new Full or Light Node based on the config
func NewNode(
	ctx context.Context,
	conf config.NodeConfig,
	appClient proxy.ClientCreator,
	genesis *cmtypes.GenesisDoc,
	metricsProvider MetricsProvider,
	logger log.Logger,
) (Node, error) {
	return newFullNode(
		ctx,
		conf,
		appClient,
		genesis,
		metricsProvider,
		logger,
	)
}
