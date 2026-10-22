// Copyright 2026 The Kaia Authors
// This file is part of the kaia library.
// SPDX-License-Identifier: LGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/kaiachain/kaia/blockchain"
	"github.com/kaiachain/kaia/blockchain/state"
	"github.com/kaiachain/kaia/blockchain/types"
	"github.com/kaiachain/kaia/blockchain/vm"
	"github.com/kaiachain/kaia/common"
	"github.com/kaiachain/kaia/common/hexutil"
	"github.com/kaiachain/kaia/networks/rpc"
	"github.com/kaiachain/kaia/params"
)

// SimulateV1Args supports one ordered block of calls. Unsupported options fail explicitly.
type SimulateV1Args struct {
	BlockStateCalls []struct {
		Calls          []simulateCall    `json:"calls"`
		StateOverrides *EthStateOverride `json:"stateOverrides,omitempty"`
		BlockOverrides json.RawMessage   `json:"blockOverrides,omitempty"`
	} `json:"blockStateCalls"`
	Validation             bool `json:"validation"`
	TraceTransfers         bool `json:"traceTransfers"`
	ReturnFullTransactions bool `json:"returnFullTransactions"`
}

// TransactionHash selects the original signed target from the local pool.
// Other call fields are ignored when it is present.
type simulateCall struct {
	EthTransactionArgs
	TransactionHash *common.Hash `json:"transactionHash,omitempty"`
}

type simulateCallResult struct {
	ReturnData hexutil.Bytes      `json:"returnData"`
	Logs       []*types.Log       `json:"logs"`
	GasUsed    hexutil.Uint64     `json:"gasUsed"`
	Status     hexutil.Uint64     `json:"status"`
	Error      *simulateCallError `json:"error,omitempty"`
}

type simulateCallError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type simulationBackend interface {
	PrepareSimulation(*types.Header, *state.StateDB) (*types.Header, error)
}

// SimulateV1 executes calls in order on private state. It never commits state or broadcasts transactions.
func (api *EthAPI) SimulateV1(ctx context.Context, args SimulateV1Args, block *rpc.BlockNumberOrHash) ([]map[string]interface{}, error) {
	if len(args.BlockStateCalls) != 1 || len(args.BlockStateCalls[0].Calls) == 0 || len(args.BlockStateCalls[0].Calls) > 64 {
		return nil, fmt.Errorf("simulateV1 requires one block containing 1 to 64 calls")
	}
	calls := args.BlockStateCalls[0]
	if args.TraceTransfers || args.ReturnFullTransactions || len(calls.BlockOverrides) != 0 {
		return nil, fmt.Errorf("simulateV1 does not support transfer tracing, full transactions, or block overrides")
	}
	b := api.kaiaBlockChainAPI.b
	prepare, ok := b.(simulationBackend)
	if !ok {
		return nil, fmt.Errorf("simulation backend unavailable")
	}
	timeout := b.RPCEVMTimeout()
	if timeout <= 0 || timeout > 3*time.Second {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	latest := rpc.LatestBlockNumber
	selector := rpc.BlockNumberOrHash{BlockNumber: &latest}
	if block != nil {
		selector = *block
	}
	baseState, parent, err := b.StateAndHeaderByNumberOrHash(ctx, selector)
	if err != nil {
		return nil, err
	}
	if baseState == nil || parent == nil {
		return nil, fmt.Errorf("simulation state unavailable")
	}
	st := baseState.Copy()
	if err := calls.StateOverrides.Apply(st); err != nil {
		return nil, err
	}
	header, err := prepare.PrepareSimulation(parent, st)
	if err != nil {
		return nil, err
	}
	gasCap := uint64(50_000_000)
	if cap := b.RPCGasCap(); cap != nil && cap.Sign() > 0 && cap.Uint64() < gasCap {
		gasCap = cap.Uint64()
	}
	results := make([]simulateCallResult, 0, len(calls.Calls))
	var used uint64
	for i, call := range calls.Calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if call.ChainID != nil && call.ChainID.ToInt().Cmp(b.ChainConfig().ChainID) != 0 {
			return nil, fmt.Errorf("call %d: incorrect chain ID", i)
		}
		intrinsic, err := types.IntrinsicGas(call.data(), call.GetAccessList(), call.GetAuthorizationList(), call.To == nil, b.ChainConfig().Rules(header.Number))
		if err != nil {
			return nil, fmt.Errorf("call %d: %w", i, err)
		}
		baseFee := header.BaseFee
		if baseFee == nil {
			baseFee = new(big.Int)
		}
		template, err := call.ToMessage(gasCap-used, baseFee, intrinsic)
		if err != nil {
			return nil, fmt.Errorf("call %d: %w", i, err)
		}
		nonce := st.GetNonce(call.from())
		if call.Nonce != nil {
			nonce = uint64(*call.Nonce)
		}
		var feeCap, tipCap *big.Int
		if call.MaxFeePerGas != nil {
			feeCap = call.MaxFeePerGas.ToInt()
			tipCap = new(big.Int)
			if call.MaxPriorityFeePerGas != nil {
				tipCap = call.MaxPriorityFeePerGas.ToInt()
			}
		}
		msg := types.NewMessage(call.from(), call.To, nonce, template.Value(), template.Gas(), template.GasPrice(), feeCap, tipCap, nil, call.data(), args.Validation, intrinsic, call.GetAccessList(), b.ChainConfig().ChainID, nil, nil, call.GetAuthorizationList())

		if call.TransactionHash != nil {
			original := b.GetPoolTransaction(*call.TransactionHash)
			if original == nil {
				return nil, fmt.Errorf("call %d: pending transaction unavailable", i)
			}
			raw, err := original.MarshalBinary()
			if err != nil {
				return nil, err
			}
			signed := new(types.Transaction)
			if err := signed.UnmarshalBinary(raw); err != nil {
				return nil, err
			}
			signer := types.MakeSigner(b.ChainConfig(), header.Number)
			msg, err = signed.AsMessageWithAccountKeyPicker(signer, st, header.Number.Uint64())
			if err != nil {
				return nil, fmt.Errorf("call %d: %w", i, err)
			}
			if err := msg.Validate(st, signer, header.Number.Uint64(), false); err != nil {
				return nil, err
			}
		}
		if used >= gasCap || msg.Gas() > gasCap-used || msg.Gas() < intrinsic {
			return nil, fmt.Errorf("call %d: simulation gas limit exceeded", i)
		}
		hash := common.BigToHash(big.NewInt(int64(i + 1)))
		st.SetTxContext(hash, common.Hash{}, i)
		evm, vmError, err := b.GetEVM(ctx, msg, st, header, vm.Config{ComputationCostLimit: params.OpcodeComputationCostLimitInfinite})
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { evm.Cancel(vm.CancelByCtxDone) })
		result, err := blockchain.ApplyMessage(evm, msg)
		stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("call %d: %w", i, err)
		}
		if err := vmError(); err != nil {
			return nil, err
		}
		if err := st.Error(); err != nil {
			return nil, err
		}
		entry := simulateCallResult{ReturnData: result.ReturnData, Logs: st.GetLogs(hash), GasUsed: hexutil.Uint64(result.UsedGas), Status: 1}
		if entry.Logs == nil {
			entry.Logs = []*types.Log{}
		}
		for _, log := range entry.Logs {
			log.BlockNumber = header.Number.Uint64()
		}
		if result.Failed() {
			entry.Status = 0
			entry.Error = &simulateCallError{Code: -32000, Message: result.Unwrap().Error()}
		}
		used += result.UsedGas
		results = append(results, entry)
		st.Finalise(true, true)
	}
	return []map[string]interface{}{{"number": (*hexutil.Big)(header.Number), "timestamp": (*hexutil.Big)(header.Time), "parentHash": parent.Hash(), "gasLimit": hexutil.Uint64(gasCap), "gasUsed": hexutil.Uint64(used), "baseFeePerGas": (*hexutil.Big)(header.BaseFee), "calls": results}}, nil
}
