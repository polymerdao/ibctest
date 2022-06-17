package ibctest

import (
	"context"
	"testing"

	"github.com/strangelove-ventures/ibctest/ibc"
	"github.com/strangelove-ventures/ibctest/testreporter"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestInterchainQueries(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pool, network := DockerSetup(t)

	rep := testreporter.NewNopReporter()
	eRep := rep.RelayerExecReporter(t)

	ctx := context.Background()

	// TODO still need to get a docker image pulled into heighliner for simd
	cf := NewBuiltinChainFactory(zaptest.NewLogger(t), []*ChainSpec{
		{Name: "simd", ChainName: "test-1", Version: ""},
		{Name: "simd", ChainName: "test-2", Version: ""},
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)

	chain1, chain2 := chains[0], chains[1]

	r := NewBuiltinRelayerFactory(ibc.CosmosRly, zaptest.NewLogger(t)).Build(
		t, pool, network, home,
	)

	const pathName = "test1-test2"
	const relayerName = "relayer"

	ic := NewInterchain().
		AddChain(chain1).
		AddChain(chain2).
		AddRelayer(r, relayerName).
		AddLink(InterchainLink{
			Chain1:  chain1,
			Chain2:  chain2,
			Relayer: r,
			Path:    pathName,
		})

	require.NoError(t, ic.Build(ctx, eRep, InterchainBuildOptions{
		TestName:  t.Name(),
		HomeDir:   home,
		Pool:      pool,
		NetworkID: network,

		SkipPathCreation: true, // skip automatic path creation because it defaults to an ics20 channel
	}))

	// TODO we skipped path creation via the Interchain API since it defaults to opening an
	// ics20 token transfer channel. So now we need to open a channel from the src chain for ICQ.

	//const userFunds = int64(10_000_000_000)
	//users := GetAndFundTestUsers(t, ctx, t.Name(), userFunds, chain1, chain2)

	//channels, err := r.GetChannels(ctx, eRep, chain1.Config().ChainID)
	//require.NoError(t, err)

	err = r.StartRelayer(ctx, eRep, pathName)
	require.NoError(t, err)

	t.Cleanup(
		func() {
			err := r.StopRelayer(ctx, eRep)
			if err != nil {
				t.Log("There was an error stopping the relayer.")
			}
			for _, c := range chains {
				if err = c.Cleanup(ctx); err != nil {
					t.Logf("There was an error cleaning up chain %s. \n", c.Config().ChainID)
				}
			}
		},
	)

	// TODO the chains and the relayer are running now and we should have a channel created so,
	// now we can perform the actual tests around querying.
}
