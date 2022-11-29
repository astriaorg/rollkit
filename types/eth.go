package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

func (b *Block) ToEthHeader(responses *tmstate.ABCIResponses) (*ethtypes.Header, error) {
	h := b.Header
	tmHash := h.Hash()
	temp := map[string]interface{}{
		"number":           hexutil.Uint64(h.Height),
		"tm_hash":          hexutil.Bytes(tmHash[:]),
		"parentHash":       common.BytesToHash(h.LastHeaderHash[:]),
		"nonce":            ethtypes.BlockNonce{},   // PoW specific
		"sha3Uncles":       ethtypes.EmptyUncleHash, // No uncles in Tendermint
		"stateRoot":        hexutil.Bytes(h.AppHash[:]),
		"logsBloom":        ethtypes.Bloom{},
		"miner":            hexutil.Bytes(h.ProposerAddress),
		"mixHash":          common.Hash{},
		"difficulty":       (*hexutil.Big)(big.NewInt(0)),
		"extraData":        "0x",
		"size":             hexutil.Uint64(0),
		"gasLimit":         hexutil.Uint64(0),
		"gasUsed":          hexutil.Uint64(0),
		"timestamp":        hexutil.Uint64(h.Time),
		"transactionsRoot": common.BytesToHash(h.DataHash[:]),
		"receiptsRoot":     ethtypes.EmptyRootHash,
		"uncles":           []common.Hash{},
		"totalDifficulty":  (*hexutil.Big)(big.NewInt(0)),
		"baseFeePerGas":    (*hexutil.Big)(big.NewInt(0)),
	}
	blockJson, err := json.Marshal(temp)
	if err != nil {
		return nil, err
	}
	var ethHeader ethtypes.Header
	err = json.Unmarshal(blockJson, &ethHeader)
	if err != nil {
		return nil, err
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
