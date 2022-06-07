package polkadot

import (
	"context"
	cRand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/StirlingMarketingGroup/go-namecase"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/icza/dyno"
	p2pCrypto "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/strangelove-ventures/ibc-test-framework/dockerutil"
	"github.com/strangelove-ventures/ibc-test-framework/ibc"
	"golang.org/x/sync/errgroup"
)

type PolkadotChain struct {
	testName           string
	cfg                ibc.ChainConfig
	numRelayChainNodes int
	parachainConfig    []ParachainConfig
	RelayChainNodes    RelayChainNodes
	ParachainNodes     []ParachainNodes
}

type PolkadotAuthority struct {
	Grandpa            string `json:"grandpa"`
	Babe               string `json:"babe"`
	IMOnline           string `json:"im_online"`
	ParachainValidator string `json:"parachain_validator"`
	AuthorityDiscovery string `json:"authority_discovery"`
	ParaValidator      string `json:"para_validator"`
	ParaAssignment     string `json:"para_assignment"`
	Beefy              string `json:"beefy"`
}

type PolkadotParachainSpec struct {
	GenesisHead    string `json:"genesis_head"`
	ValidationCode string `json:"validation_code"`
	Parachain      bool   `json:"parachain"`
}

type ParachainConfig struct {
	ChainID         string
	Bin             string
	Image           ibc.ChainDockerImage
	NumNodes        int
	Flags           []string
	RelayChainFlags []string
}

var IndexedName = []string{"alice", "bob", "charlie", "dave", "ferdie"}

func NewPolkadotChainConfig() ibc.ChainConfig {
	return ibc.ChainConfig{
		Type:         "polkadot",
		Name:         "polkadot",
		Bech32Prefix: "",
		Denom:        "uDOT",
		// TODO maybe use these params for the weight-based fee model
		GasPrices:      "",
		GasAdjustment:  0,
		TrustingPeriod: "",
		Images: []ibc.ChainDockerImage{
			{
				Repository: "ghcr.io/strangelove-ventures/heighliner/polkadot",
			},
		},
		Bin: "polkadot",
	}
}

func NewPolkadotChain(testName string, chainConfig ibc.ChainConfig, numRelayChainNodes int, parachains []ParachainConfig) *PolkadotChain {
	return &PolkadotChain{
		testName:           testName,
		cfg:                chainConfig,
		numRelayChainNodes: numRelayChainNodes,
		parachainConfig:    parachains,
	}
}

// fetch chain configuration
func (c *PolkadotChain) Config() ibc.ChainConfig {
	return c.cfg
}

// initializes node structs so that things like initializing keys can be done before starting the chain
func (c *PolkadotChain) Initialize(testName string, home string, pool *dockertest.Pool, networkID string) error {
	relayChainNodes := []*RelayChainNode{}
	chainCfg := c.Config()
	images := []ibc.ChainDockerImage{}
	images = append(images, chainCfg.Images...)
	for _, parachain := range c.parachainConfig {
		images = append(images, parachain.Image)
	}
	for _, image := range images {
		err := pool.Client.PullImage(docker.PullImageOptions{
			Repository: image.Repository,
			Tag:        image.Version,
		}, docker.AuthConfiguration{})
		if err != nil {
			fmt.Printf("error pulling image %s:%s : %v", image.Repository, image.Version, err)
		}
	}
	for i := 0; i < c.numRelayChainNodes; i++ {
		seed := make([]byte, 32)
		rand.Read(seed)

		nodeKey, _, err := p2pCrypto.GenerateEd25519Key(cRand.Reader)
		if err != nil {
			return fmt.Errorf("error generating node key: %w", err)
		}

		nameCased := namecase.New().NameCase(IndexedName[i])

		ed25519PrivKey, err := DeriveEd25519FromName(nameCased)
		if err != nil {
			return err
		}
		accountKey, err := DeriveSr25519FromName([]string{nameCased})
		if err != nil {
			return err
		}
		stashKey, err := DeriveSr25519FromName([]string{nameCased, "stash"})
		if err != nil {
			return err
		}
		ecdsaPrivKey, err := DeriveSecp256k1FromName(nameCased)
		if err != nil {
			return fmt.Errorf("error generating secp256k1 private key: %w", err)
		}
		pn := &RelayChainNode{
			Home:              home,
			Index:             i,
			Chain:             c,
			Pool:              pool,
			NetworkID:         networkID,
			TestName:          testName,
			Image:             chainCfg.Images[0],
			NodeKey:           nodeKey,
			Ed25519PrivateKey: ed25519PrivKey,
			AccountKey:        accountKey,
			StashKey:          stashKey,
			EcdsaPrivateKey:   *ecdsaPrivKey,
		}

		pn.MkDir()
		relayChainNodes = append(relayChainNodes, pn)
	}
	c.RelayChainNodes = relayChainNodes
	for _, parachainConfig := range c.parachainConfig {
		parachainNodes := []*ParachainNode{}
		for i := 0; i < parachainConfig.NumNodes; i++ {
			nodeKey, _, err := p2pCrypto.GenerateEd25519Key(cRand.Reader)
			if err != nil {
				return fmt.Errorf("error generating node key: %w", err)
			}
			pn := &ParachainNode{
				Home:            home,
				Index:           i,
				Chain:           c,
				Pool:            pool,
				NetworkID:       networkID,
				TestName:        testName,
				NodeKey:         nodeKey,
				Image:           parachainConfig.Image,
				Bin:             parachainConfig.Bin,
				ChainID:         parachainConfig.ChainID,
				Flags:           parachainConfig.Flags,
				RelayChainFlags: parachainConfig.RelayChainFlags,
			}
			pn.MkDir()
			parachainNodes = append(parachainNodes, pn)
		}
		c.ParachainNodes = append(c.ParachainNodes, parachainNodes)
	}

	return nil
}

func runtimeGenesisPath(path ...interface{}) []interface{} {
	fullPath := []interface{}{"genesis", "runtime", "runtime_genesis_config"}
	fullPath = append(fullPath, path...)
	return fullPath
}

func (c *PolkadotChain) modifyGenesis(ctx context.Context, chainSpec interface{}) error {
	bootNodes := []string{}
	authorities := [][]interface{}{}
	balances := [][]interface{}{}
	var sudoAddress string
	for i, n := range c.RelayChainNodes {
		multiAddress, err := n.MultiAddress()
		if err != nil {
			return err
		}
		bootNodes = append(bootNodes, multiAddress)
		stashAddress, err := n.StashAddress()
		if err != nil {
			return fmt.Errorf("error getting stash address")
		}
		accountAddress, err := n.AccountAddress()
		if err != nil {
			return fmt.Errorf("error getting account address")
		}
		grandpaAddress, err := n.GrandpaAddress()
		if err != nil {
			return fmt.Errorf("error getting grandpa address")
		}
		beefyAddress, err := n.EcdsaAddress()
		if err != nil {
			return fmt.Errorf("error getting beefy address")
		}
		balances = append(balances,
			[]interface{}{stashAddress, 1000000000000000000},
			[]interface{}{accountAddress, 1000000000000000000},
		)
		if i == 0 {
			sudoAddress = accountAddress
		}
		authority := []interface{}{stashAddress, stashAddress, PolkadotAuthority{
			Grandpa:            grandpaAddress,
			Babe:               accountAddress,
			IMOnline:           accountAddress,
			ParachainValidator: accountAddress,
			AuthorityDiscovery: accountAddress,
			ParaValidator:      accountAddress,
			ParaAssignment:     accountAddress,
			Beefy:              beefyAddress,
		}}
		authorities = append(authorities, authority)
	}

	if err := dyno.Set(chainSpec, bootNodes, "bootNodes"); err != nil {
		return fmt.Errorf("error setting boot nodes: %w", err)
	}
	if err := dyno.Set(chainSpec, authorities, runtimeGenesisPath("session", "keys")...); err != nil {
		return fmt.Errorf("error setting authorities: %w", err)
	}
	if err := dyno.Set(chainSpec, balances, runtimeGenesisPath("balances", "balances")...); err != nil {
		return fmt.Errorf("error setting balances: %w", err)
	}
	if err := dyno.Set(chainSpec, sudoAddress, runtimeGenesisPath("sudo", "key")...); err != nil {
		return fmt.Errorf("error setting sudo key: %w", err)
	}
	if err := dyno.Set(chainSpec, sudoAddress, runtimeGenesisPath("bridgeRococoGrandpa", "owner")...); err != nil {
		return fmt.Errorf("error setting bridgeRococoGrandpa owner: %w", err)
	}
	if err := dyno.Set(chainSpec, sudoAddress, runtimeGenesisPath("bridgeWococoGrandpa", "owner")...); err != nil {
		return fmt.Errorf("error setting bridgeWococoGrandpa owner: %w", err)
	}
	if err := dyno.Set(chainSpec, sudoAddress, runtimeGenesisPath("bridgeRococoMessages", "owner")...); err != nil {
		return fmt.Errorf("error setting bridgeRococoMessages owner: %w", err)
	}
	if err := dyno.Set(chainSpec, sudoAddress, runtimeGenesisPath("bridgeWococoMessages", "owner")...); err != nil {
		return fmt.Errorf("error setting bridgeWococoMessages owner: %w", err)
	}
	if err := dyno.Set(chainSpec, 2, runtimeGenesisPath("configuration", "config", "validation_upgrade_delay")...); err != nil {
		return fmt.Errorf("error setting validation upgrade delay: %w", err)
	}
	parachains := [][]interface{}{}

	for _, parachainNodes := range c.ParachainNodes {
		firstParachainNode := parachainNodes[0]
		parachainID, err := firstParachainNode.ParachainID(ctx)
		if err != nil {
			return fmt.Errorf("error getting parachain ID: %w", err)
		}
		genesisState, err := firstParachainNode.ExportGenesisState(ctx, parachainID)
		if err != nil {
			return fmt.Errorf("error exporting genesis state: %w", err)
		}
		genesisWasm, err := firstParachainNode.ExportGenesisWasm(ctx)
		if err != nil {
			return fmt.Errorf("error exporting genesis wasm: %w", err)
		}

		composableParachain := []interface{}{parachainID, PolkadotParachainSpec{
			GenesisHead:    genesisState,
			ValidationCode: genesisWasm,
			Parachain:      true,
		}}
		parachains = append(parachains, composableParachain)
	}

	if err := dyno.Set(chainSpec, parachains, runtimeGenesisPath("paras", "paras")...); err != nil {
		return fmt.Errorf("error setting parachains: %w", err)
	}
	return nil
}

// sets up everything needed (validators, gentx, fullnodes, peering, additional accounts) for chain to start from genesis
func (c *PolkadotChain) Start(testName string, ctx context.Context, additionalGenesisWallets ...ibc.WalletAmount) error {
	// generate chain spec
	firstNode := c.RelayChainNodes[0]
	if err := firstNode.GenerateChainSpec(ctx); err != nil {
		return fmt.Errorf("error generating chain spec: %w", err)
	}
	chainSpecBytes, err := os.ReadFile(firstNode.ChainSpecFilePath())
	if err != nil {
		return fmt.Errorf("error reading chain spec: %w", err)
	}

	var chainSpec interface{}
	if err := json.Unmarshal(chainSpecBytes, &chainSpec); err != nil {
		return fmt.Errorf("error unmarshaling chain spec: %w", err)
	}

	if err := c.modifyGenesis(ctx, chainSpec); err != nil {
		return fmt.Errorf("error modifying genesis: %w", err)
	}

	editedChainSpec, err := json.MarshalIndent(chainSpec, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling modified chain spec: %w", err)
	}
	if err := os.WriteFile(firstNode.ChainSpecFilePath(), editedChainSpec, 0644); err != nil {
		return fmt.Errorf("error writing modified chain spec: %w", err)
	}

	fmt.Printf("{%s} => generating raw chain spec...\n", firstNode.Name())
	if err := firstNode.GenerateChainSpecRaw(ctx); err != nil {
		return err
	}

	rawChainSpecFilePath := firstNode.RawChainSpecFilePath()
	var eg errgroup.Group
	for i, n := range c.RelayChainNodes {
		n := n
		i := i
		eg.Go(func() error {
			if i != 0 {
				fmt.Printf("{%s} => copying raw chain spec...\n", n.Name())
				if _, err := dockerutil.CopyFile(rawChainSpecFilePath, n.RawChainSpecFilePath()); err != nil {
					return err
				}
			}
			fmt.Printf("{%s} => creating container...\n", n.Name())
			if err := n.CreateNodeContainer(); err != nil {
				return err
			}
			fmt.Printf("{%s} => starting container...\n", n.Name())
			return n.StartContainer(ctx)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	for _, nodes := range c.ParachainNodes {
		nodes := nodes
		for _, n := range nodes {
			n := n
			eg.Go(func() error {
				fmt.Printf("{%s} => copying raw chain spec...\n", n.Name())
				if _, err := dockerutil.CopyFile(rawChainSpecFilePath, n.RawChainSpecFilePath()); err != nil {
					return err
				}
				fmt.Printf("{%s} => creating container...\n", n.Name())
				if err := n.CreateNodeContainer(); err != nil {
					return err
				}
				fmt.Printf("{%s} => starting container...\n", n.Name())
				return n.StartContainer(ctx)
			})
		}
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

// start a chain with a provided genesis file. Will override validators for first 2/3 of voting power
func (c *PolkadotChain) StartWithGenesisFile(testName string, ctx context.Context, home string, pool *dockertest.Pool, networkID string, genesisFilePath string) error {
	return errors.New("not implemented yet")
}

// export state at specific height
func (c *PolkadotChain) ExportState(ctx context.Context, height int64) (string, error) {
	return "", errors.New("not implemented yet")
}

// retrieves rpc address that can be reached by other containers in the docker network
func (c *PolkadotChain) GetRPCAddress() string {
	return ""
}

// retrieves grpc address that can be reached by other containers in the docker network
func (c *PolkadotChain) GetGRPCAddress() string {
	return ""
}

// GetHostRPCAddress returns the rpc address that can be reached by processes on the host machine.
// Note that this will not return a valid value until after Start returns.
func (c *PolkadotChain) GetHostRPCAddress() string {
	return ""
}

// GetHostGRPCAddress returns the grpc address that can be reached by processes on the host machine.
// Note that this will not return a valid value until after Start returns.
func (c *PolkadotChain) GetHostGRPCAddress() string {
	return ""
}

// get current height
func (c *PolkadotChain) Height() (int64, error) {
	return -1, errors.New("not implemented yet")
}

// creates a test key in the "user" node, (either the first fullnode or the first validator if no fullnodes)
func (c *PolkadotChain) CreateKey(ctx context.Context, keyName string) error {
	return errors.New("not implemented yet")
}

// fetches the bech32 address for a test key on the "user" node (either the first fullnode or the first validator if no fullnodes)
func (c *PolkadotChain) GetAddress(ctx context.Context, keyName string) ([]byte, error) {
	return []byte{}, errors.New("not implemented yet")
}

// send funds to wallet from user account
func (c *PolkadotChain) SendFunds(ctx context.Context, keyName string, amount ibc.WalletAmount) error {
	return errors.New("not implemented yet")
}

// sends an IBC transfer from a test key on the "user" node (either the first fullnode or the first validator if no fullnodes)
// returns tx hash
func (c *PolkadotChain) SendIBCTransfer(ctx context.Context, channelID, keyName string, amount ibc.WalletAmount, timeout *ibc.IBCTimeout) (string, error) {
	return "", errors.New("not implemented yet")
}

// takes file path to smart contract and initialization message. returns contract address
func (c *PolkadotChain) InstantiateContract(ctx context.Context, keyName string, amount ibc.WalletAmount, fileName, initMessage string, needsNoAdminFlag bool) (string, error) {
	return "", errors.New("not implemented yet")
}

// executes a contract transaction with a message using it's address
func (c *PolkadotChain) ExecuteContract(ctx context.Context, keyName string, contractAddress string, message string) error {
	return errors.New("not implemented yet")
}

// dump state of contract at block height
func (c *PolkadotChain) DumpContractState(ctx context.Context, contractAddress string, height int64) (*ibc.DumpContractStateResponse, error) {
	return nil, errors.New("not implemented yet")
}

// create balancer pool
func (c *PolkadotChain) CreatePool(ctx context.Context, keyName string, contractAddress string, swapFee float64, exitFee float64, assets []ibc.WalletAmount) error {
	return errors.New("not implemented yet")
}

// waits for # of blocks to be produced. Returns latest height
func (c *PolkadotChain) WaitForBlocks(number int64) (int64, error) {
	time.Sleep(120 * time.Second)
	return -1, errors.New("not implemented yet")
}

// fetch balance for a specific account address and denom
func (c *PolkadotChain) GetBalance(ctx context.Context, address string, denom string) (int64, error) {
	return -1, errors.New("not implemented yet")
}

// get the fees in native denom for an amount of spent gas
func (c *PolkadotChain) GetGasFeesInNativeDenom(gasPaid int64) int64 {
	return -1
}

// fetch transaction
func (c *PolkadotChain) GetTransaction(ctx context.Context, txHash string) (*types.TxResponse, error) {
	return nil, errors.New("not implemented yet")
}

func (c *PolkadotChain) GetPacketAcknowledgment(ctx context.Context, portID, channelID string, seq uint64) (ibc.PacketAcknowledgment, error) {
	return ibc.PacketAcknowledgment{}, errors.New("not implemented yet")
}

func (c *PolkadotChain) GetPacketSequence(ctx context.Context, txHash string) (uint64, error) {
	return 0, errors.New("not implemented yet")
}

func (c *PolkadotChain) Cleanup(ctx context.Context) error {
	var eg errgroup.Group
	for _, p := range c.RelayChainNodes {
		p := p
		eg.Go(func() error {
			if err := p.StopContainer(); err != nil {
				return err
			}
			return p.Cleanup(ctx)
		})
	}
	for _, n := range c.ParachainNodes {
		for _, p := range n {
			p := p
			eg.Go(func() error {
				if err := p.StopContainer(); err != nil {
					return err
				}
				return p.Cleanup(ctx)
			})
		}
	}
	return eg.Wait()
}
