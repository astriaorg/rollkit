package config

import (
	cmcfg "github.com/cometbft/cometbft/config"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	flagDAStartHeight = "rollkit.da_start_height"

	flagAstriaGrpcListen       = "rollkit.astria_grpc_listen"
	flagAstriaSeqAddress       = "rollkit.astria_seq_addr"
	flagAstriaSeqPrivate       = "rollkit.astria_seq_private"
	flagAstriaSeqInitialHeight = "rollkit.astria_seq_initial_height"
	flagDAVariance             = "rollkit.da_variance"
)

// NodeConfig stores Rollkit node configuration.
type NodeConfig struct {
	// parameters below are translated from existing config
	RootDir string
	DBPath  string
	P2P     P2PConfig
	RPC     RPCConfig
	// parameters below are Rollkit specific and read from config
	BlockManagerConfig `mapstructure:",squash"`
	Instrumentation    *cmcfg.InstrumentationConfig `mapstructure:"instrumentation"`

	Astria AstriaSeqConfig
}

// BlockManagerConfig consists of all parameters required by BlockManagerConfig
type BlockManagerConfig struct {
	// The first block height of celestia chain to use for rollup transactions.
	DAStartHeight uint64 `mapstructure:"da_start_height"`
	// The allowed variance in celestia for sequencer blocks to have been posted.
	DAVariance uint64 `mapstrcuture:"da_variance"`
}

type AstriaSeqConfig struct {
	GrpcListen       string `mapstructure:"astria_grpc_listen"`
	SeqAddress       string `mapstructure:"astria_seq_addr"`
	SeqPrivate       string `mapstructure:"astria_seq_private"`
	SeqInitialHeight uint64 `mapstructure:"astria_seq_initial_height"`
}

// GetNodeConfig translates Tendermint's configuration into Rollkit configuration.
//
// This method only translates configuration, and doesn't verify it. If some option is missing in Tendermint's
// config, it's skipped during translation.
func GetNodeConfig(nodeConf *NodeConfig, cmConf *cmcfg.Config) {
	if cmConf != nil {
		nodeConf.RootDir = cmConf.RootDir
		nodeConf.DBPath = cmConf.DBPath
		if cmConf.P2P != nil {
			nodeConf.P2P.ListenAddress = cmConf.P2P.ListenAddress
			nodeConf.P2P.Seeds = cmConf.P2P.Seeds
		}
		if cmConf.RPC != nil {
			nodeConf.RPC.ListenAddress = cmConf.RPC.ListenAddress
			nodeConf.RPC.CORSAllowedOrigins = cmConf.RPC.CORSAllowedOrigins
			nodeConf.RPC.CORSAllowedMethods = cmConf.RPC.CORSAllowedMethods
			nodeConf.RPC.CORSAllowedHeaders = cmConf.RPC.CORSAllowedHeaders
			nodeConf.RPC.MaxOpenConnections = cmConf.RPC.MaxOpenConnections
			nodeConf.RPC.TLSCertFile = cmConf.RPC.TLSCertFile
			nodeConf.RPC.TLSKeyFile = cmConf.RPC.TLSKeyFile
		}
		if cmConf.Instrumentation != nil {
			nodeConf.Instrumentation = cmConf.Instrumentation
		}
	}
}

// GetViperConfig reads configuration parameters from Viper instance.
//
// This method is called in cosmos-sdk.
func (nc *NodeConfig) GetViperConfig(v *viper.Viper) error {
	nc.DAStartHeight = v.GetUint64(flagDAStartHeight)
	nc.DAVariance = v.GetUint64(flagDAVariance)

	nc.Astria.GrpcListen = v.GetString(flagAstriaGrpcListen)
	nc.Astria.SeqAddress = v.GetString(flagAstriaSeqAddress)
	nc.Astria.SeqPrivate = v.GetString(flagAstriaSeqPrivate)
	nc.Astria.SeqInitialHeight = v.GetUint64(flagAstriaSeqInitialHeight)

	return nil
}

// AddFlags adds Rollkit specific configuration options to cobra Command.
//
// This function is called in cosmos-sdk.
func AddFlags(cmd *cobra.Command) {
	def := DefaultNodeConfig
	cmd.Flags().String(flagAstriaGrpcListen, def.Astria.GrpcListen, "Astria gRPC listen address for execution api")
	cmd.Flags().String(flagAstriaSeqAddress, def.Astria.SeqAddress, "Astria sequencer address")
	cmd.Flags().String(flagAstriaSeqPrivate, def.Astria.SeqPrivate, "Astria sequencer private key")
	cmd.Flags().Uint64(flagDAStartHeight, def.DAStartHeight, "The first block height of celestia chain to use for rollup transactions")
	cmd.Flags().Uint64(flagAstriaSeqInitialHeight, def.Astria.SeqInitialHeight, "The first block height of sequencer chain to use for rollup transactions")
	cmd.Flags().Uint64(flagDAVariance, def.DAVariance, "The allowed variance in celestia for sequencer blocks to have been posted")
}
