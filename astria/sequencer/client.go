package sequencer

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"

	astriaPb "buf.build/gen/go/astria/astria/protocolbuffers/go/astria/sequencer/v1"
	"buf.build/gen/go/astria/composer-apis/grpc/go/astria/composer/v1alpha1/composerv1alpha1grpc"
	astriaComposerPb "buf.build/gen/go/astria/composer-apis/protocolbuffers/go/astria/composer/v1alpha1"
	"github.com/astriaorg/go-sequencer-client/client"
	"github.com/cometbft/cometbft/libs/log"
	tendermintPb "github.com/cometbft/cometbft/rpc/core/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	addr = flag.String("addr", "127.0.0.1:5053", "the address to connect to")
)

// SequencerClient is a client for interacting with the sequencer.
type Client struct {
	Client         *client.Client
	composerClient *grpc.ClientConn
	Signer         *client.Signer
	rollupId       []byte
	nonce          uint32
	logger         log.Logger
}

func NewClient(sequencerAddr string, composerAddr string, private ed25519.PrivateKey, rollupId []byte, logger log.Logger) *Client {
	c, err := client.NewClient(sequencerAddr)

	// Signer for the sequencer client
	signer := client.NewSigner(private)
	if err != nil {
		panic(err)
	}
	// TODO: get addr from env
	conn, err := grpc.Dial(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	return &Client{
		Client:         c,
		composerClient: conn,
		Signer:         signer,
		rollupId:       rollupId,
		logger:         logger,
	}
}

// TODO: remove support for direct sequencer tx submission
func (c *Client) BroadcastTx(tx []byte) (*tendermintPb.ResultBroadcastTx, error) {
	unsigned := &astriaPb.UnsignedTransaction{
		Nonce: c.nonce,
		Actions: []*astriaPb.Action{
			{
				Value: &astriaPb.Action_SequenceAction{
					SequenceAction: &astriaPb.SequenceAction{
						RollupId: c.rollupId[:],
						Data:     tx,
					},
				},
			},
		},
	}

	signed, err := c.Signer.SignTransaction(unsigned)
	if err != nil {
		return nil, err
	}

	signedJson, _ := protojson.Marshal(signed)
	c.logger.Info("Submitting tx to sequencer", "signedTx", signedJson)

	resp, err := c.Client.BroadcastTxSync(context.Background(), signed)
	if err != nil {
		return nil, err
	}

	if resp.Code == 4 {
		// fetch new nonce
		newNonce, err := c.Client.GetNonce(context.Background(), c.Signer.Address())
		if err != nil {
			return nil, err
		}
		c.nonce = newNonce

		// create new tx
		unsigned = &astriaPb.UnsignedTransaction{
			Nonce:   c.nonce,
			Actions: unsigned.Actions,
		}
		signed, err = c.Signer.SignTransaction(unsigned)
		if err != nil {
			return nil, err
		}

		// submit new tx
		resp, err = c.Client.BroadcastTxSync(context.Background(), signed)
		if err != nil {
			return nil, err
		}
		if resp.Code != 0 {
			fmt.Println(resp)
			return nil, fmt.Errorf("unexpected error code: %d", resp.Code)
		}
	} else if resp.Code != 0 {
		fmt.Println(resp)
		return nil, fmt.Errorf("unexpected error code: %d", resp.Code)
	}

	return resp, nil
}

func (sc *Client) SendMessageViaComposer(tx []byte) error {

	grpcCollectorServiceClient := composerv1alpha1grpc.NewGrpcCollectorServiceClient(sc.composerClient)
	// if the request succeeds, then an empty response will be returned which can be ignored for now
	resp, err := grpcCollectorServiceClient.SubmitRollupTransaction(context.Background(), &astriaComposerPb.SubmitRollupTransactionRequest{
		RollupId: sc.rollupId,
		Data:     tx,
	})
	sc.logger.Info("Sending through composer grpc collector", "respond", resp.String())
	if err != nil {
		return err
	}

	return nil

}
