package config

import (
	"time"

	"github.com/cometbft/cometbft/config"
)

const (
	// DefaultListenAddress is a default listen address for P2P client.
	DefaultListenAddress = "/ip4/0.0.0.0/tcp/7676"
	// Version is the current rollkit version
	// Please keep updated with each new release
	Version = "0.13.0"
)

// DefaultNodeConfig keeps default values of NodeConfig
var DefaultNodeConfig = NodeConfig{
	P2P: P2PConfig{
		ListenAddress: DefaultListenAddress,
		Seeds:         "",
	},
	Aggregator:     false,
	LazyAggregator: false,
	BlockManagerConfig: BlockManagerConfig{
		BlockTime:   1 * time.Second,
		DABlockTime: 15 * time.Second,
	},
	DAAddress:  ":26650",
	DAGasPrice: -1,
	Light:      false,
	HeaderConfig: HeaderConfig{
		TrustedHash: "",
	},
	Instrumentation: config.DefaultInstrumentationConfig(),
	Astria: AstriaSeqConfig{
		GrpcListen: ":50051",
		SeqAddress: "http://localhost:26658",
		SeqPrivate: "2bd806c97f0e00af1a1fc3328fa763a9269723c8db8fac4f93af71db186d6e90",
	},
}
