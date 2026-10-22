package api

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	mock_api "github.com/kaiachain/kaia/api/mocks"
	"github.com/kaiachain/kaia/blockchain"
	"github.com/kaiachain/kaia/blockchain/state"
	"github.com/kaiachain/kaia/blockchain/types"
	"github.com/kaiachain/kaia/blockchain/vm"
	"github.com/kaiachain/kaia/common"
	"github.com/kaiachain/kaia/common/hexutil"
	"github.com/kaiachain/kaia/params"
	"github.com/kaiachain/kaia/storage/database"
	"github.com/stretchr/testify/require"
)

type simulateTestBackend struct{ *mock_api.MockBackend }

func (b *simulateTestBackend) PrepareSimulation(h *types.Header, _ *state.StateDB) (*types.Header, error) {
	return types.CopyHeader(h), nil
}

func TestSimulateV1OrderedStateAndFailure(t *testing.T) {
	config := *params.TestChainConfig
	sender := common.HexToAddress("0xaaaa")
	contract := common.HexToAddress("0xbbbb")
	revert := common.HexToAddress("0xcccc")
	// Increment slot zero, emit the new value, and return it.
	code := hexutil.MustDecode("0x6000546001018060005560005260206000a060206000f3")
	dbm := database.NewMemoryDBManager()
	genesis := (&blockchain.Genesis{Config: &config, Alloc: blockchain.GenesisAlloc{
		sender:   {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)},
		contract: {Balance: new(big.Int), Code: code},
		revert:   {Balance: new(big.Int), Code: hexutil.MustDecode("0x60006000fd")},
	}}).MustCommit(dbm)
	st, err := state.New(genesis.Root(), state.NewDatabase(dbm), nil, nil)
	require.NoError(t, err)
	header := genesis.Header()
	chain := &testChainContext{header: header}
	backend := &simulateTestBackend{mock_api.NewMockBackend(gomock.NewController(t))}
	any := gomock.Any()
	backend.EXPECT().ChainConfig().Return(&config).AnyTimes()
	backend.EXPECT().RPCEVMTimeout().Return(time.Second).AnyTimes()
	backend.EXPECT().RPCGasCap().Return(big.NewInt(1_000_000)).AnyTimes()
	backend.EXPECT().StateAndHeaderByNumberOrHash(any, any).Return(st, header, nil).AnyTimes()
	backend.EXPECT().GetEVM(any, any, any, any, any).DoAndReturn(func(_ context.Context, msg blockchain.Message, s *state.StateDB, h *types.Header, cfg vm.Config) (*vm.EVM, func() error, error) {
		return vm.NewEVM(blockchain.NewEVMBlockContext(h, chain, nil), blockchain.NewEVMTxContext(msg, h, &config), s, &config, &cfg), func() error { return nil }, nil
	}).AnyTimes()
	api := &EthAPI{kaiaBlockChainAPI: NewKaiaBlockChainAPI(backend)}
	var args SimulateV1Args
	require.NoError(t, json.Unmarshal([]byte(`{"blockStateCalls":[{"calls":[{"from":"0x000000000000000000000000000000000000aaaa","to":"0x000000000000000000000000000000000000bbbb","gas":"0x186a0"},{"from":"0x000000000000000000000000000000000000aaaa","to":"0x000000000000000000000000000000000000cccc","gas":"0x186a0"},{"from":"0x000000000000000000000000000000000000aaaa","to":"0x000000000000000000000000000000000000bbbb","gas":"0x186a0"}]}]}`), &args))
	result, err := api.SimulateV1(context.Background(), args, nil)
	require.NoError(t, err)
	calls := result[0]["calls"].([]simulateCallResult)
	require.Equal(t, int64(1), new(big.Int).SetBytes(calls[0].ReturnData).Int64())
	require.Equal(t, int64(2), new(big.Int).SetBytes(calls[2].ReturnData).Int64())
	require.Len(t, calls[0].Logs, 1)
	require.Len(t, calls[2].Logs, 1)
	require.Empty(t, calls[1].Logs)
	require.Zero(t, calls[1].Status)
	require.NotNil(t, calls[1].Error)
	require.Equal(t, common.Hash{}, st.GetState(contract, common.Hash{}), "simulation must not change source state")
	args.Validation = true
	badNonce := hexutil.Uint64(100)
	args.BlockStateCalls[0].Calls[0].Nonce = &badNonce
	_, err = api.SimulateV1(context.Background(), args, nil)
	require.ErrorContains(t, err, "nonce too high")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = api.SimulateV1(ctx, args, nil)
	require.ErrorIs(t, err, context.Canceled)
}
