package json

import (
	"context"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmconfig "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/proxy"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	cmtypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/astriaorg/rollkit/config"
	"github.com/astriaorg/rollkit/node"
	"github.com/astriaorg/rollkit/test/mocks"
)

func prepareProposalResponse(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	return &abci.ResponsePrepareProposal{
		Txs: req.Txs,
	}, nil
}

var MockServerAddr = ":7980"

// copied from rpc
func getRPC(t *testing.T) (*mocks.Application, rpcclient.Client) {
	t.Helper()
	require := require.New(t)
	app := &mocks.Application{}
	app.On("InitChain", mock.Anything, mock.Anything).Return(&abci.ResponseInitChain{}, nil)
	app.On("PrepareProposal", mock.Anything, mock.Anything).Return(prepareProposalResponse).Maybe()
	app.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
	app.On("FinalizeBlock", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
			txResults := make([]*abci.ExecTxResult, len(req.Txs))
			for idx := range req.Txs {
				txResults[idx] = &abci.ExecTxResult{
					Code: abci.CodeTypeOK,
				}
			}

			return &abci.ResponseFinalizeBlock{
				TxResults: txResults,
			}, nil
		},
	)
	app.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil)
	app.On("CheckTx", mock.Anything, mock.Anything).Return(&abci.ResponseCheckTx{
		GasWanted: 1000,
		GasUsed:   1000,
	}, nil)
	app.On("Info", mock.Anything, mock.Anything).Return(&abci.ResponseInfo{
		Data:             "mock",
		Version:          "mock",
		AppVersion:       123,
		LastBlockHeight:  345,
		LastBlockAppHash: nil,
	}, nil)

	n, err := node.NewNode(context.Background(), config.NodeConfig{BlockManagerConfig: config.BlockManagerConfig{}}, proxy.NewLocalClientCreator(app), &cmtypes.GenesisDoc{ChainID: "test"}, node.DefaultMetricsProvider(cmconfig.DefaultInstrumentationConfig()), log.TestingLogger())
	require.NoError(err)
	require.NotNil(n)

	err = n.Start()
	require.NoError(err)

	local := n.GetClient()
	require.NotNil(local)

	return app, local
}
