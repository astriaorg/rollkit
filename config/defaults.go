package config

import (
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
	BlockManagerConfig: BlockManagerConfig{
		DAStartHeight: 1,
		DAVariance:    3600,
	},
	Instrumentation: config.DefaultInstrumentationConfig(),
	Astria: AstriaSeqConfig{
		GrpcListen:       ":50051",
		SeqAddress:       "http://localhost:26657",
		ComposerRpc:      "http://127.0.0.1:5053",
		SeqPrivate:       "2bd806c97f0e00af1a1fc3328fa763a9269723c8db8fac4f93af71db186d6e90",
		SeqInitialHeight: 1,
	},
}
