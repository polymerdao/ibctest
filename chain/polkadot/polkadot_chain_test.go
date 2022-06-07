package polkadot_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/strangelove-ventures/ibctest"
)

func TestPolkadotComposableChainStart(t *testing.T) {
	t.Parallel()

	pool, network := ibctest.DockerSetup(t)
	home := t.TempDir() // Must be before chain cleanup to avoid test error during cleanup.

	log := zap.NewNop()
	chain, err := ibctest.BuiltinChainFactoryEntry{
		Name:          "polkadot",
		Version:       "polkadot:v0.9.19,composable:v2.1.9",
		ChainID:       "rococo-local",
		NumValidators: 5,
		NumFullNodes:  3,
	}.GetChain(log, t.Name())
	require.NoError(t, err, "failed to get polkadot chain")

	ctx := context.Background()

	t.Cleanup(func() {
		if err := chain.Cleanup(ctx); err != nil {
			log.Warn("chain cleanup failed", zap.String("chain", chain.Config().ChainID), zap.Error(err))
		}
	})

	err = chain.Initialize(t.Name(), home, pool, network)
	require.NoError(t, err, "failed to initialize polkadot chain")

	err = chain.Start(t.Name(), ctx)
	require.NoError(t, err, "failed to start polkadot chain")

	// TODO
	// _, err = chain.WaitForBlocks(10)
	// require.NoError(t, err, "polkadot chain failed to make blocks")
	time.Sleep(2 * time.Minute)
}

func TestPolkadotComposableBasiliskChainStart(t *testing.T) {
	t.Parallel()

	pool, network := ibctest.DockerSetup(t)
	home := t.TempDir() // Must be before chain cleanup to avoid test error during cleanup.

	log := zap.NewNop()
	chain, err := ibctest.BuiltinChainFactoryEntry{
		Name:          "polkadot",
		Version:       "polkadot:v0.9.19,composable:v2.1.9,basilisk:v7.0.1",
		ChainID:       "rococo-local",
		NumValidators: 5,
		NumFullNodes:  2,
	}.GetChain(log, t.Name())
	require.NoError(t, err, "failed to get polkadot chain")

	ctx := context.Background()

	t.Cleanup(func() {
		if err := chain.Cleanup(ctx); err != nil {
			log.Warn("chain cleanup failed", zap.String("chain", chain.Config().ChainID), zap.Error(err))
		}
	})

	err = chain.Initialize(t.Name(), home, pool, network)
	require.NoError(t, err, "failed to initialize polkadot chain")

	err = chain.Start(t.Name(), ctx)
	require.NoError(t, err, "failed to start polkadot chain")

	// TODO
	// _, err = chain.WaitForBlocks(10)
	// require.NoError(t, err, "polkadot chain failed to make blocks")
	time.Sleep(2 * time.Minute)
}
