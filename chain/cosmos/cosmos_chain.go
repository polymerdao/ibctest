package cosmos

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/cosmos/cosmos-sdk/types"
	authTx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	bankTypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	chanTypes "github.com/cosmos/ibc-go/v3/modules/core/04-channel/types"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/strangelove-ventures/ibctest/chain/internal/tendermint"
	"github.com/strangelove-ventures/ibctest/ibc"
	"github.com/strangelove-ventures/ibctest/internal/blockdb"
	"github.com/strangelove-ventures/ibctest/test"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type CosmosChain struct {
	testName      string
	cfg           ibc.ChainConfig
	numValidators int
	numFullNodes  int
	ChainNodes    ChainNodes

	log *zap.Logger
}

func NewCosmosHeighlinerChainConfig(name string,
	binary string,
	bech32Prefix string,
	denom string,
	gasPrices string,
	gasAdjustment float64,
	trustingPeriod string,
	noHostMount bool) ibc.ChainConfig {
	return ibc.ChainConfig{
		Type:           "cosmos",
		Name:           name,
		Bech32Prefix:   bech32Prefix,
		Denom:          denom,
		GasPrices:      gasPrices,
		GasAdjustment:  gasAdjustment,
		TrustingPeriod: trustingPeriod,
		NoHostMount:    noHostMount,
		Images: []ibc.DockerImage{
			{
				Repository: fmt.Sprintf("ghcr.io/strangelove-ventures/heighliner/%s", name),
			},
		},
		Bin: binary,
	}
}

func NewCosmosChain(testName string, chainConfig ibc.ChainConfig, numValidators int, numFullNodes int, log *zap.Logger) *CosmosChain {
	return &CosmosChain{
		testName:      testName,
		cfg:           chainConfig,
		numValidators: numValidators,
		numFullNodes:  numFullNodes,
		log:           log,
	}
}

// Implements Chain interface
func (c *CosmosChain) Config() ibc.ChainConfig {
	return c.cfg
}

// Implements Chain interface
func (c *CosmosChain) Initialize(testName string, homeDirectory string, cli *client.Client, networkID string) error {
	c.initializeChainNodes(testName, homeDirectory, cli, networkID)
	return nil
}

func (c *CosmosChain) getFullNode() *ChainNode {
	if len(c.ChainNodes) > c.numValidators {
		// use first full node
		return c.ChainNodes[c.numValidators]
	}
	// use first validator
	return c.ChainNodes[0]
}

// Exec implements ibc.Chain.
func (c *CosmosChain) Exec(ctx context.Context, cmd []string, env []string) (stdout, stderr []byte, err error) {
	return c.getFullNode().Exec(ctx, cmd, env)
}

// Implements Chain interface
func (c *CosmosChain) GetRPCAddress() string {
	return fmt.Sprintf("http://%s:26657", c.getFullNode().HostName())
}

// Implements Chain interface
func (c *CosmosChain) GetGRPCAddress() string {
	return fmt.Sprintf("%s:9090", c.getFullNode().HostName())
}

// GetHostRPCAddress returns the address of the RPC server accessible by the host.
// This will not return a valid address until the chain has been started.
func (c *CosmosChain) GetHostRPCAddress() string {
	return "http://" + c.getFullNode().hostRPCPort
}

// GetHostGRPCAddress returns the address of the gRPC server accessible by the host.
// This will not return a valid address until the chain has been started.
func (c *CosmosChain) GetHostGRPCAddress() string {
	return c.getFullNode().hostGRPCPort
}

// HomeDir implements ibc.Chain.
func (c *CosmosChain) HomeDir() string {
	return c.getFullNode().HomeDir()
}

// Implements Chain interface
func (c *CosmosChain) CreateKey(ctx context.Context, keyName string) error {
	return c.getFullNode().CreateKey(ctx, keyName)
}

// Implements Chain interface
func (c *CosmosChain) RecoverKey(ctx context.Context, keyName, mnemonic string) error {
	return c.getFullNode().RecoverKey(ctx, keyName, mnemonic)
}

// Implements Chain interface
func (c *CosmosChain) GetAddress(ctx context.Context, keyName string) ([]byte, error) {
	keyInfo, err := c.getFullNode().Keybase().Key(keyName)
	if err != nil {
		return []byte{}, err
	}

	return keyInfo.GetAddress().Bytes(), nil
}

// Implements Chain interface
func (c *CosmosChain) SendFunds(ctx context.Context, keyName string, amount ibc.WalletAmount) error {
	return c.getFullNode().SendFunds(ctx, keyName, amount)
}

// Implements Chain interface
func (c *CosmosChain) SendIBCTransfer(ctx context.Context, channelID, keyName string, amount ibc.WalletAmount, timeout *ibc.IBCTimeout) (tx ibc.Tx, _ error) {
	txHash, err := c.getFullNode().SendIBCTransfer(ctx, channelID, keyName, amount, timeout)
	if err != nil {
		return tx, fmt.Errorf("send ibc transfer: %w", err)
	}
	txResp, err := c.getTransaction(txHash)
	if err != nil {
		return tx, fmt.Errorf("failed to get transaction %s: %w", txHash, err)
	}
	tx.Height = uint64(txResp.Height)
	tx.TxHash = txHash
	// In cosmos, user is charged for entire gas requested, not the actual gas used.
	tx.GasSpent = txResp.GasWanted

	const evType = "send_packet"
	events := txResp.Events

	var (
		seq, _           = tendermint.AttributeValue(events, evType, "packet_sequence")
		srcPort, _       = tendermint.AttributeValue(events, evType, "packet_src_port")
		srcChan, _       = tendermint.AttributeValue(events, evType, "packet_src_channel")
		dstPort, _       = tendermint.AttributeValue(events, evType, "packet_dst_port")
		dstChan, _       = tendermint.AttributeValue(events, evType, "packet_dst_channel")
		timeoutHeight, _ = tendermint.AttributeValue(events, evType, "packet_timeout_height")
		timeoutTs, _     = tendermint.AttributeValue(events, evType, "packet_timeout_timestamp")
		data, _          = tendermint.AttributeValue(events, evType, "packet_data")
	)
	tx.Packet.SourcePort = srcPort
	tx.Packet.SourceChannel = srcChan
	tx.Packet.DestPort = dstPort
	tx.Packet.DestChannel = dstChan
	tx.Packet.TimeoutHeight = timeoutHeight
	tx.Packet.Data = []byte(data)

	seqNum, err := strconv.Atoi(seq)
	if err != nil {
		return tx, fmt.Errorf("invalid packet sequence from events %s: %w", seq, err)
	}
	tx.Packet.Sequence = uint64(seqNum)

	timeoutNano, err := strconv.ParseUint(timeoutTs, 10, 64)
	if err != nil {
		return tx, fmt.Errorf("invalid packet timestamp timeout %s: %w", timeoutTs, err)
	}
	tx.Packet.TimeoutTimestamp = ibc.Nanoseconds(timeoutNano)

	return tx, nil
}

// Implements Chain interface
func (c *CosmosChain) InstantiateContract(ctx context.Context, keyName string, amount ibc.WalletAmount, fileName, initMessage string, needsNoAdminFlag bool) (string, error) {
	return c.getFullNode().InstantiateContract(ctx, keyName, amount, fileName, initMessage, needsNoAdminFlag)
}

// Implements Chain interface
func (c *CosmosChain) ExecuteContract(ctx context.Context, keyName string, contractAddress string, message string) error {
	return c.getFullNode().ExecuteContract(ctx, keyName, contractAddress, message)
}

// Implements Chain interface
func (c *CosmosChain) DumpContractState(ctx context.Context, contractAddress string, height int64) (*ibc.DumpContractStateResponse, error) {
	return c.getFullNode().DumpContractState(ctx, contractAddress, height)
}

// Implements Chain interface
func (c *CosmosChain) ExportState(ctx context.Context, height int64) (string, error) {
	return c.getFullNode().ExportState(ctx, height)
}

// Implements Chain interface
func (c *CosmosChain) CreatePool(ctx context.Context, keyName string, contractAddress string, swapFee float64, exitFee float64, assets []ibc.WalletAmount) error {
	return c.getFullNode().CreatePool(ctx, keyName, contractAddress, swapFee, exitFee, assets)
}

// Implements Chain interface
func (c *CosmosChain) GetBalance(ctx context.Context, address string, denom string) (int64, error) {
	params := &bankTypes.QueryBalanceRequest{Address: address, Denom: denom}
	grpcAddress := c.getFullNode().hostGRPCPort
	conn, err := grpc.Dial(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	queryClient := bankTypes.NewQueryClient(conn)
	res, err := queryClient.Balance(ctx, params)

	if err != nil {
		return 0, err
	}

	return res.Balance.Amount.Int64(), nil
}

func (c *CosmosChain) getTransaction(txHash string) (*types.TxResponse, error) {
	// Retry because sometimes the tx is not committed to state yet.
	var txResp *types.TxResponse
	err := retry.Do(func() error {
		var err error
		txResp, err = authTx.QueryTx(c.getFullNode().CliContext(), txHash)
		return err
	}, retry.Attempts(15), retry.Delay(200*time.Millisecond)) // retry for total of 3 seconds
	return txResp, err
}

func (c *CosmosChain) GetGasFeesInNativeDenom(gasPaid int64) int64 {
	gasPrice, _ := strconv.ParseFloat(strings.Replace(c.cfg.GasPrices, c.cfg.Denom, "", 1), 64)
	fees := float64(gasPaid) * gasPrice
	return int64(fees)
}

// creates the test node objects required for bootstrapping tests
func (c *CosmosChain) initializeChainNodes(
	testName, home string,
	cli *client.Client,
	networkID string,
) {
	var chainNodes []*ChainNode
	count := c.numValidators + c.numFullNodes
	chainCfg := c.Config()
	for _, image := range chainCfg.Images {
		rc, err := cli.ImagePull(
			context.TODO(),
			image.Repository+":"+image.Version,
			dockertypes.ImagePullOptions{},
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
		tn := &ChainNode{
			log: c.log,

			Home:         home,
			Index:        i,
			Chain:        c,
			DockerClient: cli,
			NetworkID:    networkID,
			TestName:     testName,
			Image:        chainCfg.Images[0],
		}
		tn.MkDir()
		chainNodes = append(chainNodes, tn)
	}
	c.ChainNodes = chainNodes
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

// Bootstraps the chain and starts it from genesis
func (c *CosmosChain) Start(testName string, ctx context.Context, additionalGenesisWallets ...ibc.WalletAmount) error {
	chainCfg := c.Config()

	genesisAmount := types.Coin{
		Amount: types.NewInt(1000000000000),
		Denom:  chainCfg.Denom,
	}

	genesisStakeAmount := types.Coin{
		Amount: types.NewInt(1000000000000),
		Denom:  "stake",
	}

	genesisSelfDelegation := types.Coin{
		Amount: types.NewInt(100000000000),
		Denom:  "stake",
	}

	genesisAmounts := []types.Coin{genesisAmount, genesisStakeAmount}

	validators := c.ChainNodes[:c.numValidators]
	fullnodes := c.ChainNodes[c.numValidators:]

	eg := new(errgroup.Group)
	// sign gentx for each validator
	for _, v := range validators {
		v := v
		eg.Go(func() error { return v.InitValidatorFiles(ctx, &chainCfg, genesisAmounts, genesisSelfDelegation) })
	}

	// just initialize folder for any full nodes
	for _, n := range fullnodes {
		n := n
		eg.Go(func() error { return n.InitFullNodeFiles(ctx) })
	}

	// wait for this to finish
	if err := eg.Wait(); err != nil {
		return err
	}

	// for the validators we need to collect the gentxs and the accounts
	// to the first node's genesis file
	validator0 := validators[0]
	for i := 1; i < len(validators); i++ {
		validatorN := validators[i]
		n0key, err := validatorN.GetKey(valKey)
		if err != nil {
			return err
		}

		bech32, err := types.Bech32ifyAddressBytes(chainCfg.Bech32Prefix, n0key.GetAddress().Bytes())
		if err != nil {
			return err
		}

		if err := validator0.AddGenesisAccount(ctx, bech32, genesisAmounts); err != nil {
			return err
		}
		nNid, err := validatorN.NodeID()
		if err != nil {
			return err
		}
		oldPath := filepath.Join(validatorN.Dir(), "config", "gentx", fmt.Sprintf("gentx-%s.json", nNid))
		newPath := filepath.Join(validator0.Dir(), "config", "gentx", fmt.Sprintf("gentx-%s.json", nNid))
		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}
	}

	for _, wallet := range additionalGenesisWallets {
		if err := validator0.AddGenesisAccount(ctx, wallet.Address, []types.Coin{{Denom: wallet.Denom, Amount: types.NewInt(wallet.Amount)}}); err != nil {
			return err
		}
	}

	if err := validator0.CollectGentxs(ctx); err != nil {
		return err
	}

	genbz, err := os.ReadFile(validator0.GenesisFilePath())
	if err != nil {
		return err
	}

	for i := 1; i < len(c.ChainNodes); i++ {
		if err := os.WriteFile(c.ChainNodes[i].GenesisFilePath(), genbz, 0644); err != nil { //nolint
			return err
		}
	}

	if err := c.ChainNodes.LogGenesisHashes(); err != nil {
		return err
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range c.ChainNodes {
		n := n
		eg.Go(func() error {
			return n.CreateNodeContainer(egCtx)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	peers := c.ChainNodes.PeerString()

	for _, n := range c.ChainNodes {
		n := n
		c.log.Info("Starting container", zap.String("container", n.Name()))
		eg.Go(func() error {
			n.SetValidatorConfigAndPeers(peers)
			return n.StartContainer(ctx)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	// Wait for 5 blocks before considering the chains "started"
	return test.WaitForBlocks(ctx, 5, c.getFullNode())
}

func (c *CosmosChain) Cleanup(ctx context.Context) error {
	// noop
	return nil
}

// Height implements ibc.Chain
func (c *CosmosChain) Height(ctx context.Context) (uint64, error) {
	return c.getFullNode().Height(ctx)
}

// RegisterInterchainAccount will register an interchain account on behalf of the calling chain (controller chain)
// on the counterparty chain (the host chain).
func (c *CosmosChain) RegisterInterchainAccount(ctx context.Context, keyName, connectionID string) (string, error) {
	return c.getFullNode().RegisterICA(ctx, keyName, connectionID)
}

// SendICABankTransfer will send a bank transfer msg from the fromAddr to the specified address for the given amount and denom.
func (c *CosmosChain) SendICABankTransfer(ctx context.Context, connectionID, fromAddr string, amount ibc.WalletAmount) error {
	return c.getFullNode().SendICABankTransfer(ctx, connectionID, fromAddr, amount)
}

// QueryInterchainAccount will query the interchain account that was created on behalf of the specified address.
func (c *CosmosChain) QueryInterchainAccount(ctx context.Context, connectionID, address string) (string, error) {
	return c.getFullNode().QueryICA(ctx, connectionID, address)
}

// Acknowledgements implements ibc.Chain, returning all acknowledgments in block at height
func (c *CosmosChain) Acknowledgements(ctx context.Context, height uint64) ([]ibc.PacketAcknowledgement, error) {
	var acks []*chanTypes.MsgAcknowledgement
	err := rangeBlockMessages(ctx, c.getFullNode().Client, height, func(msg types.Msg) bool {
		found, ok := msg.(*chanTypes.MsgAcknowledgement)
		if ok {
			acks = append(acks, found)
		}
		return false
	})
	if err != nil {
		return nil, fmt.Errorf("find acknowledgements at height %d: %w", height, err)
	}
	ibcAcks := make([]ibc.PacketAcknowledgement, len(acks))
	for i, ack := range acks {
		ack := ack
		ibcAcks[i] = ibc.PacketAcknowledgement{
			Acknowledgement: ack.Acknowledgement,
			Packet: ibc.Packet{
				Sequence:         ack.Packet.Sequence,
				SourcePort:       ack.Packet.SourcePort,
				SourceChannel:    ack.Packet.SourceChannel,
				DestPort:         ack.Packet.DestinationPort,
				DestChannel:      ack.Packet.DestinationChannel,
				Data:             ack.Packet.Data,
				TimeoutHeight:    ack.Packet.TimeoutHeight.String(),
				TimeoutTimestamp: ibc.Nanoseconds(ack.Packet.TimeoutTimestamp),
			},
		}
	}
	return ibcAcks, nil
}

// Timeouts implements ibc.Chain, returning all timeouts in block at height
func (c *CosmosChain) Timeouts(ctx context.Context, height uint64) ([]ibc.PacketTimeout, error) {
	var timeouts []*chanTypes.MsgTimeout
	err := rangeBlockMessages(ctx, c.getFullNode().Client, height, func(msg types.Msg) bool {
		found, ok := msg.(*chanTypes.MsgTimeout)
		if ok {
			timeouts = append(timeouts, found)
		}
		return false
	})
	if err != nil {
		return nil, fmt.Errorf("find timeouts at height %d: %w", height, err)
	}
	ibcTimeouts := make([]ibc.PacketTimeout, len(timeouts))
	for i, ack := range timeouts {
		ack := ack
		ibcTimeouts[i] = ibc.PacketTimeout{
			Packet: ibc.Packet{
				Sequence:         ack.Packet.Sequence,
				SourcePort:       ack.Packet.SourcePort,
				SourceChannel:    ack.Packet.SourceChannel,
				DestPort:         ack.Packet.DestinationPort,
				DestChannel:      ack.Packet.DestinationChannel,
				Data:             ack.Packet.Data,
				TimeoutHeight:    ack.Packet.TimeoutHeight.String(),
				TimeoutTimestamp: ibc.Nanoseconds(ack.Packet.TimeoutTimestamp),
			},
		}
	}
	return ibcTimeouts, nil
}

// FindTxs implements blockdb.BlockSaver.
func (c *CosmosChain) FindTxs(ctx context.Context, height uint64) ([]blockdb.Tx, error) {
	return c.getFullNode().FindTxs(ctx, height)
}
