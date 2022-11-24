package types

import (
	"bytes"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

func (b *Block) ToEthHeader(responses *tmstate.ABCIResponses) (*ethtypes.Header, error) {
	h := b.Header
	ethHeader := ethtypes.Header{
		Number:      new(big.Int).SetUint64(h.Height),
		ParentHash:  common.BytesToHash(h.LastHeaderHash[:]),
		Nonce:       ethtypes.BlockNonce{},   // PoW specific
		UncleHash:   ethtypes.EmptyUncleHash, // No uncles in Tendermint
		Bloom:       ethtypes.Bloom{},
		Coinbase:    common.BytesToAddress(h.ProposerAddress),
		MixDigest:   common.Hash{},
		Difficulty:  big.NewInt(0),
		Extra:       []byte("0x"),
		GasLimit:    uint64(0),
		GasUsed:     uint64(0),
		Time:        h.Time,
		TxHash:      common.BytesToHash(h.DataHash[:]),
		ReceiptHash: ethtypes.EmptyRootHash,
		BaseFee:     big.NewInt(0),
	}

	var txRoot common.Hash
	for _, event := range responses.EndBlock.Events {
		if event.Type != "tx_root" {
			continue
		}
		for _, attr := range event.Attributes {
			if bytes.Equal(attr.Key, []byte("ethTxRoot")) {
				txRoot = common.HexToHash(string(attr.Value))
				break
			}
		}
	}
	fmt.Println("SaveBlock txRoot: ", txRoot)
	ethHeader.TxHash = txRoot
	// b.Header.DataHash = txRoot

	var receiptHash common.Hash
	for _, event := range responses.EndBlock.Events {
		if event.Type != "receipt_hash" {
			continue
		}
		for _, attr := range event.Attributes {
			if bytes.Equal(attr.Key, []byte("ethReceiptHash")) {
				receiptHash = common.HexToHash(string(attr.Value))
				break
			}
		}
	}
	fmt.Println("SaveBlock receiptHash: ", receiptHash)
	ethHeader.ReceiptHash = receiptHash

	var bloom ethtypes.Bloom
	for _, event := range responses.EndBlock.Events {
		if event.Type != "block_bloom" {
			continue
		}

		for _, attr := range event.Attributes {
			if bytes.Equal(attr.Key, []byte("bloom")) {
				bloom = ethtypes.BytesToBloom(attr.Value)
				break
			}
		}
	}
	ethHeader.Bloom = bloom

	gasUsed := uint64(0)
	for _, txsResult := range responses.DeliverTxs {
		// workaround for cosmos-sdk bug. https://github.com/cosmos/cosmos-sdk/issues/10832
		if txsResult.GetCode() == 11 && strings.Contains(txsResult.GetLog(), "no block gas left to run tx: out of gas") {
			// block gas limit has exceeded, other txs must have failed with same reason.
			break
		}
		gasUsed += uint64(txsResult.GetGasUsed())
	}
	ethHeader.GasUsed = gasUsed

	var gasLimit uint64
	for _, event := range responses.EndBlock.Events {
		if event.Type != "gas_limit" {
			continue
		}

		for _, attr := range event.Attributes {
			if bytes.Equal(attr.Key, []byte("ethGasLimit")) {
				gasLimit, err = strconv.ParseUint(string(attr.Value), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("failed to parse gas limit: %w", err)
				}
				break
			}
		}
	}
	ethHeader.GasLimit = uint64(uint32(gasLimit))

	return &ethHeader, nil
}
