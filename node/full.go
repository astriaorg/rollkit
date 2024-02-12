package node

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	ds "github.com/ipfs/go-datastore"
	ktds "github.com/ipfs/go-datastore/keytransform"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	llcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	proxy "github.com/cometbft/cometbft/proxy"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	cmtypes "github.com/cometbft/cometbft/types"

	"github.com/astriaorg/rollkit/astria/execution"
	astriamempool "github.com/astriaorg/rollkit/astria/mempool"
	"github.com/astriaorg/rollkit/astria/sequencer"
	"github.com/astriaorg/rollkit/block"
	"github.com/astriaorg/rollkit/config"
	"github.com/astriaorg/rollkit/mempool"
	"github.com/astriaorg/rollkit/state"
	"github.com/astriaorg/rollkit/state/indexer"
	blockidxkv "github.com/astriaorg/rollkit/state/indexer/block/kv"
	"github.com/astriaorg/rollkit/state/txindex"
	"github.com/astriaorg/rollkit/state/txindex/kv"
	"github.com/astriaorg/rollkit/store"
	"github.com/astriaorg/rollkit/types"
)

// prefixes used in KV store to separate main node data from DALC data
var (
	mainPrefix       = "0"
	indexerPrefix    = "1" // indexPrefix uses "i", so using "0-2" to avoid clash
	commitmentPrefix = "2"
)

const (
	// genesisChunkSize is the maximum size, in bytes, of each
	// chunk in the genesis structure for the chunked API
	genesisChunkSize = 16 * 1024 * 1024 // 16 MiB
)

var _ Node = &FullNode{}

// FullNode represents a client node in Rollkit network.
// It connects all the components and orchestrates their work.
type FullNode struct {
	service.BaseService

	genesis *cmtypes.GenesisDoc
	// cache of chunked genesis data.
	genChunks []string

	nodeConfig config.NodeConfig

	proxyApp proxy.AppConns
	eventBus *cmtypes.EventBus

	// TODO(tzdybal): consider extracting "mempool reactor"
	Mempool      mempool.Mempool
	Store        store.Store
	BlockManager *block.SSManager
	client       rpcclient.Client

	// Preserves cometBFT compatibility
	TxIndexer      txindex.TxIndexer
	BlockIndexer   indexer.BlockIndexer
	IndexerService *txindex.IndexerService
	prometheusSrv  *http.Server

	// keep context here only because of API compatibility
	// - it's used in `OnStart` (defined in service.Service interface)
	ctx           context.Context
	cancel        context.CancelFunc
	threadManager *types.ThreadManager

	// astria added
	reaper            *astriamempool.MempoolReaper
	grpcServerHandler *execution.GRPCServerHandler
}

// newFullNode creates a new Rollkit full node.
func newFullNode(
	ctx context.Context,
	nodeConfig config.NodeConfig,
	p2pKey crypto.PrivKey,
	signingKey crypto.PrivKey,
	clientCreator proxy.ClientCreator,
	genesis *cmtypes.GenesisDoc,
	metricsProvider MetricsProvider,
	logger log.Logger,
) (fn *FullNode, err error) {
	// Create context with cancel so that all services using the context can
	// catch the cancel signal when the node shutdowns
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		// If there is an error, cancel the context
		if err != nil {
			cancel()
		}
	}()

	seqMetrics, _, memplMetrics, smMetrics, abciMetrics := metricsProvider(genesis.ChainID)

	proxyApp, err := initProxyApp(clientCreator, logger, abciMetrics)
	if err != nil {
		return nil, err
	}

	eventBus, err := initEventBus(logger)
	if err != nil {
		return nil, err
	}

	baseKV, err := initBaseKV(nodeConfig, logger)
	if err != nil {
		return nil, err
	}

	// dalcKV := newPrefixKV(baseKV, dalcPrefix)
	// dalc, err := initDALC(nodeConfig, dalcKV, logger)
	// if err != nil {
	// 	return nil, err
	// }

	// p2pClient, err := p2p.NewClient(nodeConfig.P2P, p2pKey, genesis.ChainID, baseKV, logger.With("module", "p2p"), p2pMetrics)
	// if err != nil {
	// 	return nil, err
	// }

	mainKV := newPrefixKV(baseKV, mainPrefix)

	// headerSyncService, err := initHeaderSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger)
	// if err != nil {
	// 	return nil, err
	// }

	// blockSyncService, err := initBlockSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger)
	// if err != nil {
	// 	return nil, err
	// }

	mempool := initMempool(logger, proxyApp, memplMetrics)

	store := store.New(mainKV)
	blockManager, err := initBlockManager(signingKey, nodeConfig, genesis, store, mempool, proxyApp, eventBus, logger, seqMetrics, smMetrics)
	if err != nil {
		return nil, err
	}

	indexerKV := newPrefixKV(baseKV, indexerPrefix)
	indexerService, txIndexer, blockIndexer, err := createAndStartIndexerService(ctx, nodeConfig, indexerKV, eventBus, logger)
	if err != nil {
		return nil, err
	}

	blockManager.CheckCrashRecovery(ctx)

	// genesis info for exec api & sequencer client
	execGenesisInfo := execution.GenesisInfo{
		RollupId:                    sha256.Sum256([]byte(genesis.ChainID)),
		SequencerGenesisBlockHeight: nodeConfig.Astria.SeqInitialHeight,
		CelestiaBaseBlockHeight:     nodeConfig.DAStartHeight,
		CelestiaBlockVariance:       nodeConfig.DAVariance,
		GenesisTime:                 genesis.GenesisTime,
	}

	// init mempool reaper
	privateKeyBytes, err := hex.DecodeString(nodeConfig.Astria.SeqPrivate)
	if err != nil {
		return nil, err
	}
	private := ed25519.NewKeyFromSeed(privateKeyBytes)
	seqClient := sequencer.NewClient(nodeConfig.Astria.SeqAddress, private, execGenesisInfo.RollupId, logger.With("module", "seqclient"))
	reaper := astriamempool.NewMempoolReaper(seqClient, mempool, logger.With("module", "reaper"))

	// init grpc execution api
	serviceV1a2 := execution.NewExecutionServiceServerV1Alpha2(blockManager, execGenesisInfo, store, logger.With("module", "execution"))
	grpcServerHandler := execution.NewGRPCServerHandler(serviceV1a2, nodeConfig.Astria.GrpcListen, logger.With("module", "execution"))

	node := &FullNode{
		proxyApp:          proxyApp,
		eventBus:          eventBus,
		genesis:           genesis,
		nodeConfig:        nodeConfig,
		BlockManager:      blockManager,
		Mempool:           mempool,
		Store:             store,
		TxIndexer:         txIndexer,
		IndexerService:    indexerService,
		BlockIndexer:      blockIndexer,
		ctx:               ctx,
		cancel:            cancel,
		threadManager:     types.NewThreadManager(),
		reaper:            reaper,
		grpcServerHandler: grpcServerHandler,
	}

	node.BaseService = *service.NewBaseService(logger, "Node", node)
	node.client = NewFullClient(node)

	return node, nil
}

func initProxyApp(clientCreator proxy.ClientCreator, logger log.Logger, metrics *proxy.Metrics) (proxy.AppConns, error) {
	proxyApp := proxy.NewAppConns(clientCreator, metrics)
	proxyApp.SetLogger(logger.With("module", "proxy"))
	if err := proxyApp.Start(); err != nil {
		return nil, fmt.Errorf("error while starting proxy app connections: %v", err)
	}
	return proxyApp, nil
}

func initEventBus(logger log.Logger) (*cmtypes.EventBus, error) {
	eventBus := cmtypes.NewEventBus()
	eventBus.SetLogger(logger.With("module", "events"))
	if err := eventBus.Start(); err != nil {
		return nil, err
	}
	return eventBus, nil
}

// initBaseKV initializes the base key-value store.
func initBaseKV(nodeConfig config.NodeConfig, logger log.Logger) (ds.TxnDatastore, error) {
	if nodeConfig.RootDir == "" && nodeConfig.DBPath == "" { // this is used for testing
		logger.Info("WARNING: working in in-memory mode")
		return store.NewDefaultInMemoryKVStore()
	}
	return store.NewDefaultKVStore(nodeConfig.RootDir, nodeConfig.DBPath, "rollkit")
}

// func initDALC(nodeConfig config.NodeConfig, dalcKV ds.TxnDatastore, logger log.Logger) (*da.DAClient, error) {
// 	daClient := goDAProxy.NewClient()
// 	err := daClient.Start(nodeConfig.DAAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
// 	if err != nil {
// 		return nil, fmt.Errorf("error while establishing GRPC connection to DA layer: %w", err)
// 	}
// 	return &da.DAClient{DA: daClient, GasPrice: nodeConfig.DAGasPrice, Logger: logger.With("module", "da_client")}, nil
// }

func initMempool(logger log.Logger, proxyApp proxy.AppConns, memplMetrics *mempool.Metrics) *mempool.CListMempool {
	mempool := mempool.NewCListMempool(llcfg.DefaultMempoolConfig(), proxyApp.Mempool(), 0, mempool.WithMetrics(memplMetrics))
	mempool.EnableTxsAvailable()
	return mempool
}

// func initHeaderSyncService(ctx context.Context, mainKV ds.TxnDatastore, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, p2pClient *p2p.Client, logger log.Logger) (*block.HeaderSyncService, error) {
// 	headerSyncService, err := block.NewHeaderSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger.With("module", "HeaderSyncService"))
// 	if err != nil {
// 		return nil, fmt.Errorf("error while initializing HeaderSyncService: %w", err)
// 	}
// 	return headerSyncService, nil
// }

// func initBlockSyncService(ctx context.Context, mainKV ds.TxnDatastore, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, p2pClient *p2p.Client, logger log.Logger) (*block.BlockSyncService, error) {
// 	blockSyncService, err := block.NewBlockSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger.With("module", "BlockSyncService"))
// 	if err != nil {
// 		return nil, fmt.Errorf("error while initializing HeaderSyncService: %w", err)
// 	}
// 	return blockSyncService, nil
// }

func initBlockManager(signingKey crypto.PrivKey, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, store store.Store, mempool mempool.Mempool, proxyApp proxy.AppConns, eventBus *cmtypes.EventBus, logger log.Logger, seqMetrics *block.Metrics, execMetrics *state.Metrics) (*block.SSManager, error) {
	blockManager, err := block.NewSSManager(signingKey, nodeConfig.BlockManagerConfig, genesis, store, mempool, proxyApp, eventBus, logger.With("module", "BlockManager"), seqMetrics, execMetrics)
	if err != nil {
		return nil, fmt.Errorf("error while initializing BlockManager: %w", err)
	}
	return blockManager, nil
}

// initGenesisChunks creates a chunked format of the genesis document to make it easier to
// iterate through larger genesis structures.
func (n *FullNode) initGenesisChunks() error {
	if n.genChunks != nil {
		return nil
	}

	if n.genesis == nil {
		return nil
	}

	data, err := json.Marshal(n.genesis)
	if err != nil {
		return err
	}

	for i := 0; i < len(data); i += genesisChunkSize {
		end := i + genesisChunkSize

		if end > len(data) {
			end = len(data)
		}

		n.genChunks = append(n.genChunks, base64.StdEncoding.EncodeToString(data[i:end]))
	}

	return nil
}

// GetClient returns the RPC client for the full node.
func (n *FullNode) GetClient() rpcclient.Client {
	return n.client
}

// Cancel calls the underlying context's cancel function.
func (n *FullNode) Cancel() {
	n.cancel()
}

// startPrometheusServer starts a Prometheus HTTP server, listening for metrics
// collectors on addr.
func (n *FullNode) startPrometheusServer() *http.Server {
	srv := &http.Server{
		Addr: n.nodeConfig.Instrumentation.PrometheusListenAddr,
		Handler: promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer, promhttp.HandlerFor(
				prometheus.DefaultGatherer,
				promhttp.HandlerOpts{MaxRequestsInFlight: n.nodeConfig.Instrumentation.MaxOpenConnections},
			),
		),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// Error starting or closing listener:
			n.Logger.Error("Prometheus HTTP server ListenAndServe", "err", err)
		}
	}()
	return srv
}

// OnStart is a part of Service interface.
func (n *FullNode) OnStart() error {
	// begin prometheus metrics gathering if it is enabled
	if n.nodeConfig.Instrumentation != nil && n.nodeConfig.Instrumentation.IsPrometheusEnabled() {
		n.prometheusSrv = n.startPrometheusServer()
	}

	// n.Logger.Info("starting P2P client")
	// err := n.p2pClient.Start(n.ctx)
	// if err != nil {
	// 	return fmt.Errorf("error while starting P2P client: %w", err)
	// }

	// if err = n.hSyncService.Start(); err != nil {
	// 	return fmt.Errorf("error while starting header sync service: %w", err)
	// }

	// if err = n.bSyncService.Start(); err != nil {
	// 	return fmt.Errorf("error while starting block sync service: %w", err)
	// }

	n.grpcServerHandler.Start()
	n.reaper.Start()

	// if n.nodeConfig.Aggregator {
	// 	n.Logger.Info("working in aggregator mode", "block time", n.nodeConfig.BlockTime)
	// 	n.threadManager.Go(func() { n.BlockManager.AggregationLoop(n.ctx, n.nodeConfig.LazyAggregator) })
	// 	n.threadManager.Go(func() { n.BlockManager.BlockSubmissionLoop(n.ctx) })
	// 	n.threadManager.Go(func() { n.headerPublishLoop(n.ctx) })
	// 	n.threadManager.Go(func() { n.blockPublishLoop(n.ctx) })
	// 	return nil
	// }
	// n.threadManager.Go(func() { n.BlockManager.RetrieveLoop(n.ctx) })
	// n.threadManager.Go(func() { n.BlockManager.BlockStoreRetrieveLoop(n.ctx) })
	// n.threadManager.Go(func() { n.BlockManager.SyncLoop(n.ctx, n.cancel) })
	return nil
}

// GetGenesis returns entire genesis doc.
func (n *FullNode) GetGenesis() *cmtypes.GenesisDoc {
	return n.genesis
}

// GetGenesisChunks returns chunked version of genesis.
func (n *FullNode) GetGenesisChunks() ([]string, error) {
	err := n.initGenesisChunks()
	if err != nil {
		return nil, err
	}
	return n.genChunks, err
}

// OnStop is a part of Service interface.
func (n *FullNode) OnStop() {
	n.Logger.Info("halting full node...")
	n.cancel()
	n.threadManager.Wait()
	n.Logger.Info("shutting down full node sub services...")
	// err := n.p2pClient.Close()
	err := errors.Join(
		// err,
		// n.hSyncService.Stop(),
		// n.bSyncService.Stop(),
		n.grpcServerHandler.Stop(),
		n.reaper.Stop(),
		n.IndexerService.Stop(),
	)
	if n.prometheusSrv != nil {
		err = errors.Join(err, n.prometheusSrv.Shutdown(n.ctx))
	}
	n.Logger.Error("errors while stopping node:", "errors", err)
}

// OnReset is a part of Service interface.
func (n *FullNode) OnReset() error {
	panic("OnReset - not implemented!")
}

// SetLogger sets the logger used by node.
func (n *FullNode) SetLogger(logger log.Logger) {
	n.Logger = logger
}

// GetLogger returns logger.
func (n *FullNode) GetLogger() log.Logger {
	return n.Logger
}

// EventBus gives access to Node's event bus.
func (n *FullNode) EventBus() *cmtypes.EventBus {
	return n.eventBus
}

// AppClient returns ABCI proxy connections to communicate with application.
func (n *FullNode) AppClient() proxy.AppConns {
	return n.proxyApp
}

func newPrefixKV(kvStore ds.Datastore, prefix string) ds.TxnDatastore {
	return (ktds.Wrap(kvStore, ktds.PrefixTransform{Prefix: ds.NewKey(prefix)}).Children()[0]).(ds.TxnDatastore)
}

func createAndStartIndexerService(
	ctx context.Context,
	conf config.NodeConfig,
	kvStore ds.TxnDatastore,
	eventBus *cmtypes.EventBus,
	logger log.Logger,
) (*txindex.IndexerService, txindex.TxIndexer, indexer.BlockIndexer, error) {
	var (
		txIndexer    txindex.TxIndexer
		blockIndexer indexer.BlockIndexer
	)

	txIndexer = kv.NewTxIndex(ctx, kvStore)
	blockIndexer = blockidxkv.New(ctx, newPrefixKV(kvStore, "block_events"))

	indexerService := txindex.NewIndexerService(ctx, txIndexer, blockIndexer, eventBus, false)
	indexerService.SetLogger(logger.With("module", "txindex"))

	if err := indexerService.Start(); err != nil {
		return nil, nil, nil, err
	}

	return indexerService, txIndexer, blockIndexer, nil
}
