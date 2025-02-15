package penumbra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/strangelove-ventures/ibctest/chain/internal/tendermint"
	"github.com/strangelove-ventures/ibctest/ibc"
	"github.com/strangelove-ventures/ibctest/internal/dockerutil"
	"github.com/strangelove-ventures/ibctest/test"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type PenumbraNode struct {
	TendermintNode  *tendermint.TendermintNode
	PenumbraAppNode *PenumbraAppNode
}

type PenumbraNodes []PenumbraNode

type PenumbraChain struct {
	log           *zap.Logger
	testName      string
	cfg           ibc.ChainConfig
	numValidators int
	numFullNodes  int
	PenumbraNodes PenumbraNodes
}

type PenumbraValidatorDefinition struct {
	IdentityKey    string                           `json:"identity_key"`
	ConsensusKey   string                           `json:"consensus_key"`
	Name           string                           `json:"name"`
	Website        string                           `json:"website"`
	Description    string                           `json:"description"`
	FundingStreams []PenumbraValidatorFundingStream `json:"funding_streams"`
	SequenceNumber int64                            `json:"sequence_number"`
}

type PenumbraValidatorFundingStream struct {
	Address string `json:"address"`
	RateBPS int64  `json:"rate_bps"`
}

type PenumbraGenesisAppStateAllocation struct {
	Amount  int64  `json:"amount"`
	Denom   string `json:"denom"`
	Address string `json:"address"`
}

func NewPenumbraChainConfig() ibc.ChainConfig {
	return ibc.ChainConfig{
		Type:           "penumbra",
		Name:           "penumbra",
		Bech32Prefix:   "penumbra",
		Denom:          "upenumbra",
		GasPrices:      "0.0upenumbra",
		GasAdjustment:  1.3,
		TrustingPeriod: "672h",
		Images: []ibc.DockerImage{
			{
				Repository: "ghcr.io/strangelove-ventures/heighliner/tendermint",
			},
			{
				Repository: "ghcr.io/strangelove-ventures/heighliner/penumbra",
			},
		},
		Bin: "tendermint",
	}
}

func NewPenumbraChain(log *zap.Logger, testName string, chainConfig ibc.ChainConfig, numValidators int, numFullNodes int) *PenumbraChain {
	return &PenumbraChain{
		log:           log,
		testName:      testName,
		cfg:           chainConfig,
		numValidators: numValidators,
		numFullNodes:  numFullNodes,
	}
}

func (c *PenumbraChain) Acknowledgements(ctx context.Context, height uint64) ([]ibc.PacketAcknowledgement, error) {
	panic("implement me")
}

func (c *PenumbraChain) Timeouts(ctx context.Context, height uint64) ([]ibc.PacketTimeout, error) {
	panic("implement me")
}

// Implements Chain interface
func (c *PenumbraChain) Config() ibc.ChainConfig {
	return c.cfg
}

// Implements Chain interface
func (c *PenumbraChain) Initialize(testName string, homeDirectory string, cli *client.Client, networkID string) error {
	c.initializeChainNodes(testName, homeDirectory, cli, networkID)
	return nil
}

// Exec implements chain interface.
func (c *PenumbraChain) Exec(ctx context.Context, cmd []string, env []string) (stdout, stderr []byte, err error) {
	return c.getRelayerNode().PenumbraAppNode.Exec(ctx, cmd, env)
}

func (c *PenumbraChain) getRelayerNode() PenumbraNode {
	if len(c.PenumbraNodes) > c.numValidators {
		// use first full node
		return c.PenumbraNodes[c.numValidators]
	}
	// use first validator
	return c.PenumbraNodes[0]
}

// Implements Chain interface
func (c *PenumbraChain) GetRPCAddress() string {
	return fmt.Sprintf("http://%s:26657", c.getRelayerNode().TendermintNode.HostName())
}

// Implements Chain interface
func (c *PenumbraChain) GetGRPCAddress() string {
	return fmt.Sprintf("%s:9090", c.getRelayerNode().TendermintNode.HostName())
}

// GetHostRPCAddress returns the address of the RPC server accessible by the host.
// This will not return a valid address until the chain has been started.
func (c *PenumbraChain) GetHostRPCAddress() string {
	return "http://" + c.getRelayerNode().PenumbraAppNode.hostRPCPort
}

// GetHostGRPCAddress returns the address of the gRPC server accessible by the host.
// This will not return a valid address until the chain has been started.
func (c *PenumbraChain) GetHostGRPCAddress() string {
	return c.getRelayerNode().PenumbraAppNode.hostGRPCPort
}

func (c *PenumbraChain) HomeDir() string {
	panic(errors.New("HomeDir not implemented yet"))
}

// Implements Chain interface
func (c *PenumbraChain) CreateKey(ctx context.Context, keyName string) error {
	return c.getRelayerNode().PenumbraAppNode.CreateKey(ctx, keyName)
}

func (c *PenumbraChain) RecoverKey(ctx context.Context, name, mnemonic string) error {
	return fmt.Errorf("RecoverKey not implemented for PenumbraChain")
}

// Implements Chain interface
func (c *PenumbraChain) GetAddress(ctx context.Context, keyName string) ([]byte, error) {
	return c.getRelayerNode().PenumbraAppNode.GetAddress(ctx, keyName)
}

// Implements Chain interface
func (c *PenumbraChain) SendFunds(ctx context.Context, keyName string, amount ibc.WalletAmount) error {
	return c.getRelayerNode().PenumbraAppNode.SendFunds(ctx, keyName, amount)
}

// Implements Chain interface
func (c *PenumbraChain) SendIBCTransfer(ctx context.Context, channelID, keyName string, amount ibc.WalletAmount, timeout *ibc.IBCTimeout) (ibc.Tx, error) {
	return c.getRelayerNode().PenumbraAppNode.SendIBCTransfer(ctx, channelID, keyName, amount, timeout)
}

// Implements Chain interface
func (c *PenumbraChain) InstantiateContract(ctx context.Context, keyName string, amount ibc.WalletAmount, fileName, initMessage string, needsNoAdminFlag bool) (string, error) {
	// NOOP
	return "", errors.New("not yet implemented")
}

// Implements Chain interface
func (c *PenumbraChain) ExecuteContract(ctx context.Context, keyName string, contractAddress string, message string) error {
	// NOOP
	return errors.New("not yet implemented")
}

// Implements Chain interface
func (c *PenumbraChain) DumpContractState(ctx context.Context, contractAddress string, height int64) (*ibc.DumpContractStateResponse, error) {
	// NOOP
	return nil, errors.New("not yet implemented")
}

// Implements Chain interface
func (c *PenumbraChain) ExportState(ctx context.Context, height int64) (string, error) {
	return "", errors.New("not yet implemented")
}

// Implements Chain interface
func (c *PenumbraChain) CreatePool(ctx context.Context, keyName string, contractAddress string, swapFee float64, exitFee float64, assets []ibc.WalletAmount) error {
	// NOOP
	return errors.New("not yet implemented")
}

func (c *PenumbraChain) Height(ctx context.Context) (uint64, error) {
	return c.getRelayerNode().TendermintNode.Height(ctx)
}

// Implements Chain interface
func (c *PenumbraChain) GetBalance(ctx context.Context, address string, denom string) (int64, error) {
	return -1, errors.New("not yet implemented")
}

// Implements Chain interface
func (c *PenumbraChain) GetGasFeesInNativeDenom(gasPaid int64) int64 {
	gasPrice, _ := strconv.ParseFloat(strings.Replace(c.cfg.GasPrices, c.cfg.Denom, "", 1), 64)
	fees := float64(gasPaid) * gasPrice
	return int64(fees)
}

// creates the test node objects required for bootstrapping tests
func (c *PenumbraChain) initializeChainNodes(
	testName, home string,
	cli *client.Client,
	networkID string,
) {
	penumbraNodes := []PenumbraNode{}
	count := c.numValidators + c.numFullNodes
	chainCfg := c.Config()
	for _, image := range chainCfg.Images {
		rc, err := cli.ImagePull(
			context.TODO(),
			image.Repository+":"+image.Version,
			types.ImagePullOptions{},
		)
		if err != nil {
			c.log.Error("Failed to pull image",
				zap.Error(err),
				zap.String("repository", image.Repository),
				zap.String("tag", image.Version),
			)
		} else {
			_, _ = io.Copy(io.Discard, rc)
			_ = rc.Close()
		}
	}
	for i := 0; i < count; i++ {
		tn := &tendermint.TendermintNode{Log: c.log, Home: home, Index: i, Chain: c,
			DockerClient: cli, NetworkID: networkID, TestName: testName, Image: chainCfg.Images[0]}
		tn.MkDir()
		pn := &PenumbraAppNode{log: c.log, Home: home, Index: i, Chain: c,
			DockerClient: cli, NetworkID: networkID, TestName: testName, Image: chainCfg.Images[1]}
		pn.MkDir()
		penumbraNodes = append(penumbraNodes, PenumbraNode{TendermintNode: tn, PenumbraAppNode: pn})
	}
	c.PenumbraNodes = penumbraNodes
}

type GenesisValidatorPubKey struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
type GenesisValidators struct {
	Address string                 `json:"address"`
	Name    string                 `json:"name"`
	Power   string                 `json:"power"`
	PubKey  GenesisValidatorPubKey `json:"pub_key"`
}
type GenesisFile struct {
	Validators []GenesisValidators `json:"validators"`
}

type ValidatorWithIntPower struct {
	Address      string
	Power        int64
	PubKeyBase64 string
}

func (c *PenumbraChain) Start(testName string, ctx context.Context, additionalGenesisWallets ...ibc.WalletAmount) error {
	validators := c.PenumbraNodes[:c.numValidators]
	fullnodes := c.PenumbraNodes[c.numValidators:]

	chainCfg := c.Config()

	validatorDefinitions := make([]PenumbraValidatorDefinition, len(validators))
	allocations := make([]PenumbraGenesisAppStateAllocation, len(validators)*2)

	var eg errgroup.Group
	for i, v := range validators {
		v := v
		i := i
		eg.Go(func() error {
			if err := v.TendermintNode.InitValidatorFiles(ctx); err != nil {
				return fmt.Errorf("error initializing validator files: %v", err)
			}
			privValKeyBytes, err := os.ReadFile(v.TendermintNode.PrivValKeyFilePath())
			if err != nil {
				return fmt.Errorf("error reading tendermint privval key file: %v", err)
			}
			privValKey := tendermint.PrivValidatorKeyFile{}
			if err := json.Unmarshal(privValKeyBytes, &privValKey); err != nil {
				return fmt.Errorf("error unmarshaling tendermint privval key: %v", err)
			}
			if err := v.PenumbraAppNode.CreateKey(ctx, valKey); err != nil {
				return fmt.Errorf("error generating wallet on penumbra node: %v", err)
			}
			if err := v.PenumbraAppNode.InitValidatorFile(ctx); err != nil {
				return fmt.Errorf("error initializing validator template on penumbra node: %v", err)
			}
			validatorTemplateDefinitionFileBytes, err := os.ReadFile(v.PenumbraAppNode.ValidatorDefinitionTemplateFilePath())
			if err != nil {
				return fmt.Errorf("error reading validator definition template file: %v", err)
			}
			validatorTemplateDefinition := PenumbraValidatorDefinition{}
			if err := json.Unmarshal(validatorTemplateDefinitionFileBytes, &validatorTemplateDefinition); err != nil {
				return fmt.Errorf("error unmarshaling validator definition template key: %v", err)
			}
			validatorTemplateDefinition.ConsensusKey = privValKey.PubKey.Value
			validatorTemplateDefinition.Name = fmt.Sprintf("validator-%d", i)
			validatorTemplateDefinition.Description = fmt.Sprintf("validator-%d description", i)
			validatorTemplateDefinition.Website = fmt.Sprintf("https://validator-%d", i)

			// Assign validatorDefinitions and allocations at fixed indices to avoid data races across the error group's goroutines.
			validatorDefinitions[i] = validatorTemplateDefinition

			// self delegation
			allocations[2*i] = PenumbraGenesisAppStateAllocation{
				Amount:  100_000_000_000,
				Denom:   fmt.Sprintf("udelegation_%s", validatorTemplateDefinition.IdentityKey),
				Address: validatorTemplateDefinition.FundingStreams[0].Address,
			}
			// liquid
			allocations[2*i+1] = PenumbraGenesisAppStateAllocation{
				Amount:  1_000_000_000_000,
				Denom:   chainCfg.Denom,
				Address: validatorTemplateDefinition.FundingStreams[0].Address,
			}

			return nil
		})
	}

	for _, wallet := range additionalGenesisWallets {
		allocations = append(allocations, PenumbraGenesisAppStateAllocation{
			Address: wallet.Address,
			Denom:   wallet.Denom,
			Amount:  wallet.Amount,
		})
	}

	for _, n := range fullnodes {
		n := n
		eg.Go(func() error { return n.TendermintNode.InitFullNodeFiles(ctx) })
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("waiting to init full nodes' files: %w", err)
	}

	firstValidator := validators[0]
	if err := firstValidator.PenumbraAppNode.GenerateGenesisFile(ctx, chainCfg.ChainID, validatorDefinitions, allocations); err != nil {
		return fmt.Errorf("generating genesis file: %w", err)
	}

	// penumbra generate-testnet right now overwrites new validator keys
	for i, validator := range validators {
		if _, err := dockerutil.CopyFile(firstValidator.PenumbraAppNode.ValidatorPrivateKeyFile(i), validator.TendermintNode.PrivValKeyFilePath()); err != nil {
			return fmt.Errorf("copying private key to validator %d: %w", i, err)
		}
	}

	return c.start(testName, ctx, firstValidator.PenumbraAppNode.GenesisFile())
}

// Bootstraps the chain and starts it from genesis
func (c *PenumbraChain) start(testName string, ctx context.Context, genesisFilePath string) error {
	var tendermintNodes []*tendermint.TendermintNode
	for _, node := range c.PenumbraNodes {
		tendermintNodes = append(tendermintNodes, node.TendermintNode)
		if _, err := dockerutil.CopyFile(genesisFilePath, node.TendermintNode.GenesisFilePath()); err != nil { //nolint
			return err
		}
	}

	tmNodes := tendermint.TendermintNodes(tendermintNodes)

	if err := tmNodes.LogGenesisHashes(); err != nil {
		return err
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range c.PenumbraNodes {
		n := n
		eg.Go(func() error {
			return n.TendermintNode.CreateNodeContainer(
				egCtx,
				fmt.Sprintf("--proxy-app=tcp://%s:26658", n.PenumbraAppNode.HostName()),
				"--rpc.laddr=tcp://0.0.0.0:26657",
			)
		})
		eg.Go(func() error {
			return n.PenumbraAppNode.CreateNodeContainer(ctx)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	eg, egCtx = errgroup.WithContext(ctx)
	for _, n := range c.PenumbraNodes {
		n := n
		c.log.Info("Starting tendermint container", zap.String("container", n.TendermintNode.Name()))
		eg.Go(func() error {
			peers := tmNodes.PeerString(n.TendermintNode)
			if err := n.TendermintNode.SetConfigAndPeers(egCtx, peers); err != nil {
				return err
			}
			return n.TendermintNode.StartContainer(egCtx)
		})
		c.log.Info("Starting penumbra container", zap.String("container", n.PenumbraAppNode.Name()))
		eg.Go(func() error {
			return n.PenumbraAppNode.StartContainer(egCtx)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	// Wait for 5 blocks before considering the chains "started"
	err := test.WaitForBlocks(ctx, 5, c.getRelayerNode().TendermintNode)
	return err
}

func (c *PenumbraChain) Cleanup(ctx context.Context) error {
	var eg errgroup.Group
	for _, p := range c.PenumbraNodes {
		p := p
		eg.Go(func() error {
			if err := p.PenumbraAppNode.StopContainer(ctx); err != nil {
				return err
			}
			return p.PenumbraAppNode.Cleanup(ctx)
		})
	}
	return eg.Wait()
}

func (c *PenumbraChain) RegisterInterchainAccount(ctx context.Context, keyName, connectionID string) (string, error) {
	//TODO implement me
	panic("implement me")
}

func (c *PenumbraChain) SendICABankTransfer(ctx context.Context, connectionID, fromAddr string, amount ibc.WalletAmount) error {
	//TODO implement me
	panic("implement me")
}

func (c *PenumbraChain) QueryInterchainAccount(ctx context.Context, connectionID, address string) (string, error) {
	//TODO implement me
	panic("implement me")
}
