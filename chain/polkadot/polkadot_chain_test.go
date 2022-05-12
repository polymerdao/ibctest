package polkadot_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/strangelove-ventures/ibc-test-framework/ibctest"
)

func TestPolkadotComposableChainStart(t *testing.T) {
	t.Parallel()

	ctx, home, pool, network, err := ibctest.SetupTestRun(t)
	require.NoErrorf(t, err, "failed to set up test run")

	chain, err := ibctest.GetChain(t.Name(), "polkadot", "polkadot:v0.9.19,composable:v2.1.9", "rococo-local", 5, 3, zap.NewNop())
	require.NoError(t, err, "failed to get polkadot chain")

	err = chain.Initialize(t.Name(), home, pool, network)
	require.NoError(t, err, "failed to initialize polkadot chain")

	err = chain.Start(t.Name(), ctx)
	require.NoError(t, err, "failed to start polkadot chain")

	// TODO
	// _, err = chain.WaitForBlocks(50)
	// require.NoError(t, err, "polkadot chain failed to make blocks")
	time.Sleep(2 * time.Minute)
}

func TestPolkadotComposableBasiliskChainStart(t *testing.T) {
	t.Parallel()

	ctx, home, pool, network, err := ibctest.SetupTestRun(t)
	require.NoErrorf(t, err, "failed to set up test run")

	chain, err := ibctest.GetChain(t.Name(), "polkadot", "polkadot:v0.9.19,composable:v2.1.9,basilisk:v7.0.1", "rococo-local", 5, 2, zap.NewNop())
	require.NoError(t, err, "failed to get polkadot chain")

	err = chain.Initialize(t.Name(), home, pool, network)
	require.NoError(t, err, "failed to initialize polkadot chain")

	err = chain.Start(t.Name(), ctx)
	require.NoError(t, err, "failed to start polkadot chain")

	// TODO
	// _, err = chain.WaitForBlocks(50)
	// require.NoError(t, err, "polkadot chain failed to make blocks")
	time.Sleep(2 * time.Minute)
}
