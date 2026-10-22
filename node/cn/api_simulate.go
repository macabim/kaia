// Copyright 2026 The Kaia Authors
// SPDX-License-Identifier: LGPL-3.0-or-later

package cn

import (
	"math/big"

	"github.com/kaiachain/kaia/blockchain/state"
	"github.com/kaiachain/kaia/blockchain/types"
	"github.com/kaiachain/kaia/consensus/misc"
	"github.com/kaiachain/kaia/consensus/misc/eip4844"
)

// PrepareSimulation constructs the next execution header without signing or changing consensus state.
func (b *CNAPIBackend) PrepareSimulation(parent *types.Header, st *state.StateDB) (*types.Header, error) {
	header := types.CopyHeader(parent)
	header.ParentHash = parent.Hash()
	header.Number = new(big.Int).Add(parent.Number, big.NewInt(1))
	header.Time = new(big.Int).Add(parent.Time, big.NewInt(1))
	header.GasUsed = 0
	if b.ChainConfig().IsMagmaForkEnabled(header.Number) {
		pset := b.cn.govModule.GetParamSet(header.Number.Uint64())
		header.BaseFee = misc.NextMagmaBlockBaseFee(parent, pset.ToKip71Config())
	}
	if b.ChainConfig().IsOsakaForkEnabled(header.Number) {
		excess := eip4844.CalcExcessBlobGas(b.ChainConfig(), parent, header.Number)
		header.ExcessBlobGas = &excess
		header.BlobGasUsed = new(uint64)
	}
	b.cn.engine.Initialize(b.cn.blockchain, header, st)
	return header, st.Error()
}
