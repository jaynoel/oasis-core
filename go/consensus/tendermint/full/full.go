// Package full implements a full Tendermint consensus node.
package full

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	tmabcitypes "github.com/tendermint/tendermint/abci/types"
	tmconfig "github.com/tendermint/tendermint/config"
	tmpubsub "github.com/tendermint/tendermint/libs/pubsub"
	tmlight "github.com/tendermint/tendermint/light"
	tmmempool "github.com/tendermint/tendermint/mempool"
	tmnode "github.com/tendermint/tendermint/node"
	tmp2p "github.com/tendermint/tendermint/p2p"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	tmproxy "github.com/tendermint/tendermint/proxy"
	tmcli "github.com/tendermint/tendermint/rpc/client/local"
	tmrpctypes "github.com/tendermint/tendermint/rpc/core/types"
	tmstate "github.com/tendermint/tendermint/state"
	tmstatesync "github.com/tendermint/tendermint/statesync"
	tmtypes "github.com/tendermint/tendermint/types"
	tmdb "github.com/tendermint/tm-db"

	beaconAPI "github.com/oasisprotocol/oasis-core/go/beacon/api"
	"github.com/oasisprotocol/oasis-core/go/common/cbor"
	"github.com/oasisprotocol/oasis-core/go/common/crypto/hash"
	"github.com/oasisprotocol/oasis-core/go/common/crypto/signature"
	"github.com/oasisprotocol/oasis-core/go/common/errors"
	"github.com/oasisprotocol/oasis-core/go/common/identity"
	"github.com/oasisprotocol/oasis-core/go/common/logging"
	"github.com/oasisprotocol/oasis-core/go/common/node"
	"github.com/oasisprotocol/oasis-core/go/common/pubsub"
	cmservice "github.com/oasisprotocol/oasis-core/go/common/service"
	"github.com/oasisprotocol/oasis-core/go/common/version"
	consensusAPI "github.com/oasisprotocol/oasis-core/go/consensus/api"
	"github.com/oasisprotocol/oasis-core/go/consensus/api/transaction"
	"github.com/oasisprotocol/oasis-core/go/consensus/api/transaction/results"
	"github.com/oasisprotocol/oasis-core/go/consensus/metrics"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/abci"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/api"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/apps/supplementarysanity"
	tmbeacon "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/beacon"
	tmcommon "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/common"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/crypto"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/db"
	tmepochtime "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/epochtime"
	tmepochtimemock "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/epochtime_mock"
	tmkeymanager "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/keymanager"
	"github.com/oasisprotocol/oasis-core/go/consensus/tendermint/light"
	tmregistry "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/registry"
	tmroothash "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/roothash"
	tmscheduler "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/scheduler"
	tmstaking "github.com/oasisprotocol/oasis-core/go/consensus/tendermint/staking"
	epochtimeAPI "github.com/oasisprotocol/oasis-core/go/epochtime/api"
	genesisAPI "github.com/oasisprotocol/oasis-core/go/genesis/api"
	keymanagerAPI "github.com/oasisprotocol/oasis-core/go/keymanager/api"
	cmbackground "github.com/oasisprotocol/oasis-core/go/oasis-node/cmd/common/background"
	cmflags "github.com/oasisprotocol/oasis-core/go/oasis-node/cmd/common/flags"
	cmmetrics "github.com/oasisprotocol/oasis-core/go/oasis-node/cmd/common/metrics"
	"github.com/oasisprotocol/oasis-core/go/registry"
	registryAPI "github.com/oasisprotocol/oasis-core/go/registry/api"
	"github.com/oasisprotocol/oasis-core/go/roothash"
	roothashAPI "github.com/oasisprotocol/oasis-core/go/roothash/api"
	schedulerAPI "github.com/oasisprotocol/oasis-core/go/scheduler/api"
	stakingAPI "github.com/oasisprotocol/oasis-core/go/staking/api"
	upgradeAPI "github.com/oasisprotocol/oasis-core/go/upgrade/api"
)

const (
	// CfgABCIPruneStrategy configures the ABCI state pruning strategy.
	CfgABCIPruneStrategy = "consensus.tendermint.abci.prune.strategy"
	// CfgABCIPruneNumKept configures the amount of kept heights if pruning is enabled.
	CfgABCIPruneNumKept = "consensus.tendermint.abci.prune.num_kept"

	// CfgCheckpointerDisabled disables the ABCI state checkpointer.
	CfgCheckpointerDisabled = "consensus.tendermint.checkpointer.disabled"
	// CfgCheckpointerCheckInterval configures the ABCI state checkpointing check interval.
	CfgCheckpointerCheckInterval = "consensus.tendermint.checkpointer.check_interval"

	// CfgSentryUpstreamAddress defines nodes for which we act as a sentry for.
	CfgSentryUpstreamAddress = "consensus.tendermint.sentry.upstream_address"

	// CfgP2PPersistentPeer configures tendermint's persistent peer(s).
	CfgP2PPersistentPeer = "consensus.tendermint.p2p.persistent_peer"
	// CfgP2PPersistenPeersMaxDialPeriod configures the tendermint's persistent peer max dial period.
	CfgP2PPersistenPeersMaxDialPeriod = "consensus.tendermint.p2p.persistent_peers_max_dial_period"
	// CfgP2PDisablePeerExchange disables tendermint's peer-exchange (Pex) reactor.
	CfgP2PDisablePeerExchange = "consensus.tendermint.p2p.disable_peer_exchange"
	// CfgP2PUnconditionalPeerIDs configures tendermint's unconditional peer(s).
	CfgP2PUnconditionalPeerIDs = "consensus.tendermint.p2p.unconditional_peer_ids"

	// CfgDebugUnsafeReplayRecoverCorruptedWAL enables the debug and unsafe
	// automatic corrupted WAL recovery during replay.
	CfgDebugUnsafeReplayRecoverCorruptedWAL = "consensus.tendermint.debug.unsafe_replay_recover_corrupted_wal"

	// CfgMinGasPrice configures the minimum gas price for this validator.
	CfgMinGasPrice = "consensus.tendermint.min_gas_price"
	// CfgDebugDisableCheckTx disables CheckTx.
	CfgDebugDisableCheckTx = "consensus.tendermint.debug.disable_check_tx"

	// CfgSupplementarySanityEnabled is the supplementary sanity enabled flag.
	CfgSupplementarySanityEnabled = "consensus.tendermint.supplementarysanity.enabled"
	// CfgSupplementarySanityInterval configures the supplementary sanity check interval.
	CfgSupplementarySanityInterval = "consensus.tendermint.supplementarysanity.interval"

	// CfgConsensusStateSyncEnabled enabled consensus state sync.
	CfgConsensusStateSyncEnabled = "consensus.tendermint.state_sync.enabled"
	// CfgConsensusStateSyncConsensusNode specifies nodes exposing public consensus services which
	// are used to sync a light client.
	CfgConsensusStateSyncConsensusNode = "consensus.tendermint.state_sync.consensus_node"
	// CfgConsensusStateSyncTrustPeriod is the light client trust period.
	CfgConsensusStateSyncTrustPeriod = "consensus.tendermint.state_sync.trust_period"
	// CfgConsensusStateSyncTrustHeight is the known trusted height for the light client.
	CfgConsensusStateSyncTrustHeight = "consensus.tendermint.state_sync.trust_height"
	// CfgConsensusStateSyncTrustHash is the known trusted block header hash for the light client.
	CfgConsensusStateSyncTrustHash = "consensus.tendermint.state_sync.trust_hash"
)

const (
	// Time difference threshold used when considering if node is done with
	// initial syncing. If difference is greater than the specified threshold
	// the node is considered not yet synced.
	// NOTE: this is only used during the initial sync.
	syncWorkerLastBlockTimeDiffThreshold = 1 * time.Minute

	// tmSubscriberID is the subscriber identifier used for all internal Tendermint pubsub
	// subscriptions. If any other subscriber IDs need to be derived they will be under this prefix.
	tmSubscriberID = "oasis-core"
)

var (
	_ api.Backend = (*fullService)(nil)

	labelTendermint = prometheus.Labels{"backend": "tendermint"}

	// Flags has the configuration flags.
	Flags = flag.NewFlagSet("", flag.ContinueOnError)
)

// fullService implements a full Tendermint node.
type fullService struct { // nolint: maligned
	sync.Mutex
	cmservice.BaseBackgroundService

	ctx           context.Context
	svcMgr        *cmbackground.ServiceManager
	upgrader      upgradeAPI.Backend
	mux           *abci.ApplicationServer
	node          *tmnode.Node
	client        *tmcli.Local
	blockNotifier *pubsub.Broker
	failMonitor   *failMonitor

	stateStore tmstate.Store

	beacon        beaconAPI.Backend
	epochtime     epochtimeAPI.Backend
	keymanager    keymanagerAPI.Backend
	registry      registryAPI.Backend
	roothash      roothashAPI.Backend
	staking       stakingAPI.Backend
	scheduler     schedulerAPI.Backend
	submissionMgr consensusAPI.SubmissionManager

	serviceClients   []api.ServiceClient
	serviceClientsWg sync.WaitGroup

	genesis                  *genesisAPI.Document
	genesisProvider          genesisAPI.Provider
	identity                 *identity.Identity
	dataDir                  string
	isInitialized, isStarted bool
	startedCh                chan struct{}
	syncedCh                 chan struct{}

	startFn func() error

	nextSubscriberID uint64
}

func (t *fullService) initialized() bool {
	t.Lock()
	defer t.Unlock()

	return t.isInitialized
}

func (t *fullService) started() bool {
	t.Lock()
	defer t.Unlock()

	return t.isStarted
}

// Implements service.BackgroundService.
func (t *fullService) Start() error {
	if t.started() {
		return fmt.Errorf("tendermint: service already started")
	}

	switch t.initialized() {
	case true:
		if err := t.mux.Start(); err != nil {
			return err
		}
		if err := t.startFn(); err != nil {
			return err
		}
		if err := t.node.Start(); err != nil {
			return fmt.Errorf("tendermint: failed to start service: %w", err)
		}

		// Start event dispatchers for all the service clients.
		t.serviceClientsWg.Add(len(t.serviceClients))
		for _, svc := range t.serviceClients {
			go t.serviceClientWorker(t.ctx, svc)
		}
		// Start sync checker.
		go t.syncWorker()
		// Start block notifier.
		go t.blockNotifierWorker()
		// Optionally start metrics updater.
		if cmmetrics.Enabled() {
			go t.metrics()
		}
	case false:
		close(t.syncedCh)
	}

	t.Lock()
	t.isStarted = true
	t.Unlock()

	close(t.startedCh)

	return nil
}

// Implements service.BackgroundService.
func (t *fullService) Quit() <-chan struct{} {
	if !t.started() {
		return make(chan struct{})
	}

	return t.node.Quit()
}

// Implements service.BackgroundService.
func (t *fullService) Cleanup() {
	t.serviceClientsWg.Wait()
	t.svcMgr.Cleanup()
}

// Implements service.BackgroundService.
func (t *fullService) Stop() {
	if !t.initialized() || !t.started() {
		return
	}

	t.failMonitor.markCleanShutdown()
	if err := t.node.Stop(); err != nil {
		t.Logger.Error("Error on stopping node", err)
	}

	t.svcMgr.Stop()
	t.mux.Stop()
	t.node.Wait()
}

func (t *fullService) Started() <-chan struct{} {
	return t.startedCh
}

func (t *fullService) SupportedFeatures() consensusAPI.FeatureMask {
	return consensusAPI.FeatureServices | consensusAPI.FeatureFullNode
}

func (t *fullService) Synced() <-chan struct{} {
	return t.syncedCh
}

func (t *fullService) GetAddresses() ([]node.ConsensusAddress, error) {
	u, err := tmcommon.GetExternalAddress()
	if err != nil {
		return nil, err
	}

	var addr node.ConsensusAddress
	if err = addr.Address.UnmarshalText([]byte(u.Host)); err != nil {
		return nil, fmt.Errorf("tendermint: failed to parse external address host: %w", err)
	}
	addr.ID = t.identity.P2PSigner.Public()

	return []node.ConsensusAddress{addr}, nil
}

func (t *fullService) StateToGenesis(ctx context.Context, blockHeight int64) (*genesisAPI.Document, error) {
	blk, err := t.GetTendermintBlock(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("failed to get tendermint block",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}
	if blk == nil {
		return nil, consensusAPI.ErrNoCommittedBlocks
	}
	blockHeight = blk.Header.Height

	// Get initial genesis doc.
	genesisDoc, err := t.GetGenesisDocument(ctx)
	if err != nil {
		t.Logger.Error("failed getting genesis document",
			"err", err,
		)
		return nil, err
	}

	// Call StateToGenesis on all backends and merge the results together.
	epochtimeGenesis, err := t.epochtime.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("epochtime StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	registryGenesis, err := t.registry.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("registry StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	roothashGenesis, err := t.roothash.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("roothash StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	stakingGenesis, err := t.staking.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("staking StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	keymanagerGenesis, err := t.keymanager.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("keymanager StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	schedulerGenesis, err := t.scheduler.StateToGenesis(ctx, blockHeight)
	if err != nil {
		t.Logger.Error("scheduler StateToGenesis failure",
			"err", err,
			"block_height", blockHeight,
		)
		return nil, err
	}

	return &genesisAPI.Document{
		Height:     blockHeight,
		ChainID:    genesisDoc.ChainID,
		HaltEpoch:  genesisDoc.HaltEpoch,
		Time:       blk.Header.Time,
		EpochTime:  *epochtimeGenesis,
		Registry:   *registryGenesis,
		RootHash:   *roothashGenesis,
		Staking:    *stakingGenesis,
		KeyManager: *keymanagerGenesis,
		Scheduler:  *schedulerGenesis,
		Beacon:     genesisDoc.Beacon,
		Consensus:  genesisDoc.Consensus,
	}, nil
}

func (t *fullService) GetGenesisDocument(ctx context.Context) (*genesisAPI.Document, error) {
	return t.genesis, nil
}

func (t *fullService) RegisterHaltHook(hook func(context.Context, int64, epochtimeAPI.EpochTime)) {
	if !t.initialized() {
		return
	}

	t.mux.RegisterHaltHook(hook)
}

func (t *fullService) SubmitTx(ctx context.Context, tx *transaction.SignedTransaction) error {
	// Subscribe to the transaction being included in a block.
	data := cbor.Marshal(tx)
	query := tmtypes.EventQueryTxFor(data)
	subID := t.newSubscriberID()
	txSub, err := t.subscribe(subID, query)
	if err != nil {
		return err
	}
	if ptrSub, ok := txSub.(*tendermintPubsubBuffer).tmSubscription.(*tmpubsub.Subscription); ok && ptrSub == nil {
		t.Logger.Debug("broadcastTx: service has shut down. Cancel our context to recover")
		<-ctx.Done()
		return ctx.Err()
	}

	defer t.unsubscribe(subID, query) // nolint: errcheck

	// Subscribe to the transaction becoming invalid.
	txHash := hash.NewFromBytes(data)

	recheckCh, recheckSub, err := t.mux.WatchInvalidatedTx(txHash)
	if err != nil {
		return err
	}
	defer recheckSub.Close()

	// First try to broadcast.
	if err := t.broadcastTxRaw(data); err != nil {
		return err
	}

	// Wait for the transaction to be included in a block.
	select {
	case v := <-recheckCh:
		return v
	case v := <-txSub.Out():
		if result := v.Data().(tmtypes.EventDataTx).Result; !result.IsOK() {
			err := errors.FromCode(result.GetCodespace(), result.GetCode())
			if err == nil {
				// Fallback to an ordinary error.
				err = fmt.Errorf(result.GetLog())
			}
			return err
		}
		return nil
	case <-txSub.Cancelled():
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *fullService) broadcastTxRaw(data []byte) error {
	// We could use t.client.BroadcastTxSync but that is annoying as it
	// doesn't give you the right fields when CheckTx fails.
	mp := t.node.Mempool()

	// Submit the transaction to mempool and wait for response.
	ch := make(chan *tmabcitypes.Response, 1)
	err := mp.CheckTx(tmtypes.Tx(data), func(rsp *tmabcitypes.Response) {
		ch <- rsp
		close(ch)
	}, tmmempool.TxInfo{})
	switch err {
	case nil:
	case tmmempool.ErrTxInCache:
		// Transaction already in the mempool or was recently there.
		return consensusAPI.ErrDuplicateTx
	default:
		return fmt.Errorf("tendermint: failed to submit to local mempool: %w", err)
	}

	rsp := <-ch
	if result := rsp.GetCheckTx(); !result.IsOK() {
		err := errors.FromCode(result.GetCodespace(), result.GetCode())
		if err == nil {
			// Fallback to an ordinary error.
			err = fmt.Errorf(result.GetLog())
		}
		return err
	}

	return nil
}

func (t *fullService) newSubscriberID() string {
	return fmt.Sprintf("%s/subscriber-%d", tmSubscriberID, atomic.AddUint64(&t.nextSubscriberID, 1))
}

func (t *fullService) SubmitEvidence(ctx context.Context, evidence *consensusAPI.Evidence) error {
	var protoEv tmproto.Evidence
	if err := protoEv.Unmarshal(evidence.Meta); err != nil {
		return fmt.Errorf("tendermint: malformed evidence while unmarshalling: %w", err)
	}

	ev, err := tmtypes.EvidenceFromProto(&protoEv)
	if err != nil {
		return fmt.Errorf("tendermint: malformed evidence while converting: %w", err)
	}

	if _, err := t.client.BroadcastEvidence(ctx, ev); err != nil {
		return fmt.Errorf("tendermint: broadcast evidence failed: %w", err)
	}

	return nil
}

func (t *fullService) EstimateGas(ctx context.Context, req *consensusAPI.EstimateGasRequest) (transaction.Gas, error) {
	return t.mux.EstimateGas(req.Signer, req.Transaction)
}

func (t *fullService) subscribe(subscriber string, query tmpubsub.Query) (tmtypes.Subscription, error) {
	// Note: The tendermint documentation claims using SubscribeUnbuffered can
	// freeze the server, however, the buffered Subscribe can drop events, and
	// force-unsubscribe the channel if processing takes too long.

	subFn := func() (tmtypes.Subscription, error) {
		sub, err := t.node.EventBus().SubscribeUnbuffered(t.ctx, subscriber, query)
		if err != nil {
			return nil, err
		}
		// Oh yes, this can actually return a nil subscription even though the
		// error was also nil if the node is just shutting down.
		if sub == (*tmpubsub.Subscription)(nil) {
			return nil, context.Canceled
		}
		return newTendermintPubsubBuffer(sub), nil
	}

	if t.started() {
		return subFn()
	}

	// The node doesn't exist until it's started since, creating the node
	// triggers replay, InitChain, and etc.
	t.Logger.Debug("Subscribe: node not available yet, blocking",
		"subscriber", subscriber,
		"query", query,
	)

	// XXX/yawning: As far as I can tell just blocking here is safe as
	// ever single consumer of the API subscribes from a go routine.
	select {
	case <-t.startedCh:
	case <-t.ctx.Done():
		return nil, t.ctx.Err()
	}

	return subFn()
}

func (t *fullService) unsubscribe(subscriber string, query tmpubsub.Query) error {
	if t.started() {
		return t.node.EventBus().Unsubscribe(t.ctx, subscriber, query)
	}

	return fmt.Errorf("tendermint: unsubscribe called with no backing service")
}

func (t *fullService) RegisterApplication(app api.Application) error {
	return t.mux.Register(app)
}

func (t *fullService) SetTransactionAuthHandler(handler api.TransactionAuthHandler) error {
	return t.mux.SetTransactionAuthHandler(handler)
}

func (t *fullService) TransactionAuthHandler() consensusAPI.TransactionAuthHandler {
	return t.mux.TransactionAuthHandler()
}

func (t *fullService) SubmissionManager() consensusAPI.SubmissionManager {
	return t.submissionMgr
}

func (t *fullService) EpochTime() epochtimeAPI.Backend {
	return t.epochtime
}

func (t *fullService) Beacon() beaconAPI.Backend {
	return t.beacon
}

func (t *fullService) KeyManager() keymanagerAPI.Backend {
	return t.keymanager
}

func (t *fullService) Registry() registryAPI.Backend {
	return t.registry
}

func (t *fullService) RootHash() roothashAPI.Backend {
	return t.roothash
}

func (t *fullService) Staking() stakingAPI.Backend {
	return t.staking
}

func (t *fullService) Scheduler() schedulerAPI.Backend {
	return t.scheduler
}

func (t *fullService) GetEpoch(ctx context.Context, height int64) (epochtimeAPI.EpochTime, error) {
	if t.epochtime == nil {
		return epochtimeAPI.EpochInvalid, consensusAPI.ErrUnsupported
	}
	return t.epochtime.GetEpoch(ctx, height)
}

func (t *fullService) WaitEpoch(ctx context.Context, epoch epochtimeAPI.EpochTime) error {
	if t.epochtime == nil {
		return consensusAPI.ErrUnsupported
	}

	ch, sub := t.epochtime.WatchEpochs()
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-ch:
			if !ok {
				return context.Canceled
			}
			if e >= epoch {
				return nil
			}
		}
	}
}

func (t *fullService) GetBlock(ctx context.Context, height int64) (*consensusAPI.Block, error) {
	blk, err := t.GetTendermintBlock(ctx, height)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, consensusAPI.ErrNoCommittedBlocks
	}

	return api.NewBlock(blk), nil
}

func (t *fullService) GetSignerNonce(ctx context.Context, req *consensusAPI.GetSignerNonceRequest) (uint64, error) {
	return t.mux.TransactionAuthHandler().GetSignerNonce(ctx, req)
}

func (t *fullService) GetTransactions(ctx context.Context, height int64) ([][]byte, error) {
	blk, err := t.GetTendermintBlock(ctx, height)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, consensusAPI.ErrNoCommittedBlocks
	}

	txs := make([][]byte, 0, len(blk.Data.Txs))
	for _, v := range blk.Data.Txs {
		txs = append(txs, v[:])
	}
	return txs, nil
}

func (t *fullService) GetTransactionsWithResults(ctx context.Context, height int64) (*consensusAPI.TransactionsWithResults, error) {
	var txsWithResults consensusAPI.TransactionsWithResults

	blk, err := t.GetTendermintBlock(ctx, height)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, consensusAPI.ErrNoCommittedBlocks
	}
	for _, tx := range blk.Data.Txs {
		txsWithResults.Transactions = append(txsWithResults.Transactions, tx[:])
	}

	res, err := t.GetBlockResults(ctx, blk.Height)
	if err != nil {
		return nil, err
	}
	for txIdx, rs := range res.TxsResults {
		// Transaction result.
		result := &results.Result{
			Error: results.Error{
				Module:  rs.GetCodespace(),
				Code:    rs.GetCode(),
				Message: rs.GetLog(),
			},
		}

		// Transaction staking events.
		stakingEvents, err := tmstaking.EventsFromTendermint(
			txsWithResults.Transactions[txIdx],
			blk.Height,
			rs.Events,
		)
		if err != nil {
			return nil, err
		}
		for _, e := range stakingEvents {
			result.Events = append(result.Events, &results.Event{Staking: e})
		}

		// Transaction registry events.
		registryEvents, _, err := tmregistry.EventsFromTendermint(
			txsWithResults.Transactions[txIdx],
			blk.Height,
			rs.Events,
		)
		if err != nil {
			return nil, err
		}
		for _, e := range registryEvents {
			result.Events = append(result.Events, &results.Event{Registry: e})
		}

		// Transaction roothash events.
		roothashEvents, err := tmroothash.EventsFromTendermint(
			txsWithResults.Transactions[txIdx],
			blk.Height,
			rs.Events,
		)
		if err != nil {
			return nil, err
		}
		for _, e := range roothashEvents {
			result.Events = append(result.Events, &results.Event{RootHash: e})
		}
		txsWithResults.Results = append(txsWithResults.Results, result)
	}
	return &txsWithResults, nil
}

func (t *fullService) GetUnconfirmedTransactions(ctx context.Context) ([][]byte, error) {
	mempoolTxs := t.node.Mempool().ReapMaxTxs(-1)
	txs := make([][]byte, 0, len(mempoolTxs))
	for _, v := range mempoolTxs {
		txs = append(txs, v[:])
	}
	return txs, nil
}

func (t *fullService) GetStatus(ctx context.Context) (*consensusAPI.Status, error) {
	status := &consensusAPI.Status{
		ConsensusVersion: version.ConsensusProtocol.String(),
		Backend:          api.BackendName,
		Features:         t.SupportedFeatures(),
	}

	status.GenesisHeight = t.genesis.Height
	if t.started() {
		// Only attempt to fetch blocks in case the consensus service has started as otherwise
		// requests will block.
		genBlk, err := t.GetBlock(ctx, t.genesis.Height)
		switch err {
		case nil:
			status.GenesisHash = genBlk.Hash
		default:
			// We may not be able to fetch the genesis block in case it has been pruned.
		}

		lastRetainedHeight, err := t.GetLastRetainedVersion(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get last retained height: %w", err)
		}
		// Some pruning configurations return 0 instead of a valid block height. Clamp those to the genesis height.
		if lastRetainedHeight < t.genesis.Height {
			lastRetainedHeight = t.genesis.Height
		}
		status.LastRetainedHeight = lastRetainedHeight
		lastRetainedBlock, err := t.GetBlock(ctx, lastRetainedHeight)
		switch err {
		case nil:
			status.LastRetainedHash = lastRetainedBlock.Hash
		default:
			// Before we commit the first block, we can't load it from GetBlock. Don't give its hash in this case.
		}

		// Latest block.
		latestBlk, err := t.GetBlock(ctx, consensusAPI.HeightLatest)
		switch err {
		case nil:
			status.LatestHeight = latestBlk.Height
			status.LatestHash = latestBlk.Hash
			status.LatestTime = latestBlk.Time
			status.LatestStateRoot = latestBlk.StateRoot
		case consensusAPI.ErrNoCommittedBlocks:
			// No committed blocks yet.
		default:
			return nil, fmt.Errorf("failed to fetch current block: %w", err)
		}
	}

	// List of consensus peers.
	tmpeers := t.node.Switch().Peers().List()
	peers := make([]string, 0, len(tmpeers))
	for _, tmpeer := range tmpeers {
		p := string(tmpeer.ID()) + "@" + tmpeer.RemoteAddr().String()
		peers = append(peers, p)
	}
	status.NodePeers = peers

	// Check if the local node is in the validator set for the latest (uncommitted) block.
	valSetHeight := status.LatestHeight + 1
	if valSetHeight < status.GenesisHeight {
		valSetHeight = status.GenesisHeight
	}
	vals, err := t.stateStore.LoadValidators(valSetHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to load validator set: %w", err)
	}
	consensusPk := t.identity.ConsensusSigner.Public()
	consensusAddr := []byte(crypto.PublicKeyToTendermint(&consensusPk).Address())
	status.IsValidator = vals.HasAddress(consensusAddr)

	return status, nil
}

func (t *fullService) WatchBlocks(ctx context.Context) (<-chan *consensusAPI.Block, pubsub.ClosableSubscription, error) {
	ch, sub := t.WatchTendermintBlocks()
	mapCh := make(chan *consensusAPI.Block)
	go func() {
		defer close(mapCh)

		for {
			select {
			case tmBlk, ok := <-ch:
				if !ok {
					return
				}

				mapCh <- api.NewBlock(tmBlk)
			case <-ctx.Done():
				return
			}
		}
	}()

	return mapCh, sub, nil
}

func (t *fullService) ensureStarted(ctx context.Context) error {
	// Make sure that the Tendermint service has started so that we
	// have the client interface available.
	select {
	case <-t.startedCh:
	case <-t.ctx.Done():
		return t.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

func (t *fullService) initialize() error {
	t.Lock()
	defer t.Unlock()

	if t.isInitialized {
		return nil
	}

	if err := t.lazyInit(); err != nil {
		return err
	}

	// Apply the genesis public key blacklist.
	for _, v := range t.genesis.Consensus.Parameters.PublicKeyBlacklist {
		if err := v.Blacklist(); err != nil {
			t.Logger.Error("initialize: failed to blacklist key",
				"err", err,
				"pk", v,
			)
			return err
		}
	}

	if err := t.initEpochtime(); err != nil {
		return err
	}
	if err := t.mux.SetEpochtime(t.epochtime); err != nil {
		return err
	}

	// Initialize the rest of backends.
	var err error
	var scBeacon tmbeacon.ServiceClient
	if scBeacon, err = tmbeacon.New(t.ctx, t); err != nil {
		t.Logger.Error("initialize: failed to initialize beacon backend",
			"err", err,
		)
		return err
	}
	t.beacon = scBeacon
	t.serviceClients = append(t.serviceClients, scBeacon)

	var scKeyManager tmkeymanager.ServiceClient
	if scKeyManager, err = tmkeymanager.New(t.ctx, t); err != nil {
		t.Logger.Error("initialize: failed to initialize keymanager backend",
			"err", err,
		)
		return err
	}
	t.keymanager = scKeyManager
	t.serviceClients = append(t.serviceClients, scKeyManager)

	var scRegistry tmregistry.ServiceClient
	if scRegistry, err = tmregistry.New(t.ctx, t); err != nil {
		t.Logger.Error("initialize: failed to initialize registry backend",
			"err", err,
		)
		return err
	}
	t.registry = scRegistry
	if cmmetrics.Enabled() {
		t.svcMgr.RegisterCleanupOnly(registry.NewMetricsUpdater(t.ctx, t.registry), "registry metrics updater")
	}
	t.serviceClients = append(t.serviceClients, scRegistry)
	t.svcMgr.RegisterCleanupOnly(t.registry, "registry backend")

	var scStaking tmstaking.ServiceClient
	if scStaking, err = tmstaking.New(t.ctx, t); err != nil {
		t.Logger.Error("staking: failed to initialize staking backend",
			"err", err,
		)
		return err
	}
	t.staking = scStaking
	t.serviceClients = append(t.serviceClients, scStaking)
	t.svcMgr.RegisterCleanupOnly(t.staking, "staking backend")

	var scScheduler tmscheduler.ServiceClient
	if scScheduler, err = tmscheduler.New(t.ctx, t); err != nil {
		t.Logger.Error("scheduler: failed to initialize scheduler backend",
			"err", err,
		)
		return err
	}
	t.scheduler = scScheduler
	t.serviceClients = append(t.serviceClients, scScheduler)
	t.svcMgr.RegisterCleanupOnly(t.scheduler, "scheduler backend")

	var scRootHash tmroothash.ServiceClient
	if scRootHash, err = tmroothash.New(t.ctx, t.dataDir, t); err != nil {
		t.Logger.Error("roothash: failed to initialize roothash backend",
			"err", err,
		)
		return err
	}
	t.roothash = roothash.NewMetricsWrapper(scRootHash)
	t.serviceClients = append(t.serviceClients, scRootHash)
	t.svcMgr.RegisterCleanupOnly(t.roothash, "roothash backend")

	// Enable supplementary sanity checks when enabled.
	if viper.GetBool(CfgSupplementarySanityEnabled) {
		ssa := supplementarysanity.New(viper.GetUint64(CfgSupplementarySanityInterval))
		if err = t.RegisterApplication(ssa); err != nil {
			return fmt.Errorf("failed to register supplementary sanity check app: %w", err)
		}
	}

	return nil
}

func (t *fullService) GetLastRetainedVersion(ctx context.Context) (int64, error) {
	return t.mux.State().LastRetainedVersion()
}

func (t *fullService) GetTendermintBlock(ctx context.Context, height int64) (*tmtypes.Block, error) {
	if err := t.ensureStarted(ctx); err != nil {
		return nil, err
	}

	var tmHeight int64
	if height == consensusAPI.HeightLatest {
		// Do not let Tendermint determine the latest height (e.g., by passing nil here) as that
		// completely ignores ABCI processing so it can return a block for which local state does
		// not yet exist. Use our mux notion of latest height instead.
		tmHeight = t.mux.State().BlockHeight()
		if tmHeight == 0 {
			// No committed blocks yet.
			return nil, nil
		}
	} else {
		tmHeight = height
	}
	result, err := t.client.Block(ctx, &tmHeight)
	if err != nil {
		return nil, fmt.Errorf("tendermint: block query failed: %w", err)
	}
	return result.Block, nil
}

func (t *fullService) GetBlockResults(ctx context.Context, height int64) (*tmrpctypes.ResultBlockResults, error) {
	if t.client == nil {
		panic("client not available yet")
	}

	// As in GetTendermintBlock above, get the latest tendermint block height
	// from our mux.
	var tmHeight int64
	if height == consensusAPI.HeightLatest {
		tmHeight = t.mux.State().BlockHeight()
		if tmHeight == 0 {
			// No committed blocks yet.
			return nil, consensusAPI.ErrNoCommittedBlocks
		}
	} else {
		tmHeight = height
	}

	result, err := t.client.BlockResults(ctx, &tmHeight)
	if err != nil {
		return nil, fmt.Errorf("tendermint: block results query failed: %w", err)
	}

	return result, nil
}

func (t *fullService) WatchTendermintBlocks() (<-chan *tmtypes.Block, *pubsub.Subscription) {
	typedCh := make(chan *tmtypes.Block)
	sub := t.blockNotifier.Subscribe()
	sub.Unwrap(typedCh)

	return typedCh, sub
}

func (t *fullService) ConsensusKey() signature.PublicKey {
	return t.identity.ConsensusSigner.Public()
}

func (t *fullService) initEpochtime() error {
	var err error
	if t.genesis.EpochTime.Parameters.DebugMockBackend {
		var scEpochTime tmepochtimemock.ServiceClient
		scEpochTime, err = tmepochtimemock.New(t.ctx, t)
		if err != nil {
			t.Logger.Error("initEpochtime: failed to initialize mock epochtime backend",
				"err", err,
			)
			return err
		}
		t.epochtime = scEpochTime
		t.serviceClients = append(t.serviceClients, scEpochTime)
	} else {
		var scEpochTime tmepochtime.ServiceClient
		scEpochTime, err = tmepochtime.New(t.ctx, t, t.genesis.EpochTime.Parameters.Interval)
		if err != nil {
			t.Logger.Error("initEpochtime: failed to initialize epochtime backend",
				"err", err,
			)
			return err
		}
		t.epochtime = scEpochTime
		t.serviceClients = append(t.serviceClients, scEpochTime)
	}
	return nil
}

func (t *fullService) lazyInit() error {
	if t.isInitialized {
		return nil
	}

	var err error

	// Create Tendermint application mux.
	var pruneCfg abci.PruneConfig
	pruneStrat := viper.GetString(CfgABCIPruneStrategy)
	if err = pruneCfg.Strategy.FromString(pruneStrat); err != nil {
		return err
	}
	pruneCfg.NumKept = viper.GetUint64(CfgABCIPruneNumKept)

	appConfig := &abci.ApplicationConfig{
		DataDir:                   filepath.Join(t.dataDir, tmcommon.StateDir),
		StorageBackend:            db.GetBackendName(),
		Pruning:                   pruneCfg,
		HaltEpochHeight:           t.genesis.HaltEpoch,
		MinGasPrice:               viper.GetUint64(CfgMinGasPrice),
		OwnTxSigner:               t.identity.NodeSigner.Public(),
		DisableCheckTx:            viper.GetBool(CfgDebugDisableCheckTx) && cmflags.DebugDontBlameOasis(),
		DisableCheckpointer:       viper.GetBool(CfgCheckpointerDisabled),
		CheckpointerCheckInterval: viper.GetDuration(CfgCheckpointerCheckInterval),
		InitialHeight:             uint64(t.genesis.Height),
	}
	t.mux, err = abci.NewApplicationServer(t.ctx, t.upgrader, appConfig)
	if err != nil {
		return err
	}

	// Tendermint needs the on-disk directories to be present when
	// launched like this, so create the relevant sub-directories
	// under the node DataDir.
	tendermintDataDir := filepath.Join(t.dataDir, tmcommon.StateDir)
	if err = tmcommon.InitDataDir(tendermintDataDir); err != nil {
		return err
	}

	// Create Tendermint node.
	tenderConfig := tmconfig.DefaultConfig()
	_ = viper.Unmarshal(&tenderConfig)
	tenderConfig.SetRoot(tendermintDataDir)
	timeoutCommit := t.genesis.Consensus.Parameters.TimeoutCommit
	emptyBlockInterval := t.genesis.Consensus.Parameters.EmptyBlockInterval
	tenderConfig.Consensus.TimeoutCommit = timeoutCommit
	tenderConfig.Consensus.SkipTimeoutCommit = t.genesis.Consensus.Parameters.SkipTimeoutCommit
	tenderConfig.Consensus.CreateEmptyBlocks = true
	tenderConfig.Consensus.CreateEmptyBlocksInterval = emptyBlockInterval
	tenderConfig.Consensus.DebugUnsafeReplayRecoverCorruptedWAL = viper.GetBool(CfgDebugUnsafeReplayRecoverCorruptedWAL) && cmflags.DebugDontBlameOasis()
	tenderConfig.Instrumentation.Prometheus = true
	tenderConfig.Instrumentation.PrometheusListenAddr = ""
	tenderConfig.TxIndex.Indexer = "null"
	tenderConfig.P2P.ListenAddress = viper.GetString(tmcommon.CfgCoreListenAddress)
	tenderConfig.P2P.ExternalAddress = viper.GetString(tmcommon.CfgCoreExternalAddress)
	tenderConfig.P2P.PexReactor = !viper.GetBool(CfgP2PDisablePeerExchange)
	tenderConfig.P2P.MaxNumInboundPeers = viper.GetInt(tmcommon.CfgP2PMaxNumInboundPeers)
	tenderConfig.P2P.MaxNumOutboundPeers = viper.GetInt(tmcommon.CfgP2PMaxNumOutboundPeers)
	tenderConfig.P2P.SendRate = viper.GetInt64(tmcommon.CfgP2PSendRate)
	tenderConfig.P2P.RecvRate = viper.GetInt64(tmcommon.CfgP2PRecvRate)
	// Persistent peers need to be lowercase as p2p/transport.go:MultiplexTransport.upgrade()
	// uses a case sensitive string comparison to validate public keys.
	// Since persistent peers is expected to be in comma-delimited ID@host:port format,
	// lowercasing the whole string is ok.
	tenderConfig.P2P.PersistentPeers = strings.ToLower(strings.Join(viper.GetStringSlice(CfgP2PPersistentPeer), ","))
	tenderConfig.P2P.PersistentPeersMaxDialPeriod = viper.GetDuration(CfgP2PPersistenPeersMaxDialPeriod)
	// Unconditional peer IDs need to be lowercase as p2p/transport.go:MultiplexTransport.upgrade()
	// uses a case sensitive string comparison to validate public keys.
	// Since persistent peers is expected to be in comma-delimited ID format,
	// lowercasing the whole string is ok.
	tenderConfig.P2P.UnconditionalPeerIDs = strings.ToLower(strings.Join(viper.GetStringSlice(CfgP2PUnconditionalPeerIDs), ","))
	// Seed Ids need to be lowercase as p2p/transport.go:MultiplexTransport.upgrade()
	// uses a case sensitive string comparison to validate public keys.
	// Since Seeds is expected to be in comma-delimited ID@host:port format,
	// lowercasing the whole string is ok.
	tenderConfig.P2P.Seeds = strings.ToLower(strings.Join(viper.GetStringSlice(tmcommon.CfgP2PSeed), ","))
	tenderConfig.P2P.AddrBookStrict = !(viper.GetBool(tmcommon.CfgDebugP2PAddrBookLenient) && cmflags.DebugDontBlameOasis())
	tenderConfig.P2P.AllowDuplicateIP = viper.GetBool(tmcommon.CfgDebugP2PAllowDuplicateIP) && cmflags.DebugDontBlameOasis()
	tenderConfig.RPC.ListenAddress = ""

	sentryUpstreamAddrs := viper.GetStringSlice(CfgSentryUpstreamAddress)
	if len(sentryUpstreamAddrs) > 0 {
		t.Logger.Info("Acting as a tendermint sentry", "addrs", sentryUpstreamAddrs)

		// Append upstream addresses to persistent, private and unconditional peers.
		tenderConfig.P2P.PersistentPeers += "," + strings.ToLower(strings.Join(sentryUpstreamAddrs, ","))

		var sentryUpstreamIDs []string
		for _, addr := range sentryUpstreamAddrs {
			parts := strings.Split(addr, "@")
			if len(parts) != 2 {
				return fmt.Errorf("malformed sentry upstream address: %s", addr)
			}
			sentryUpstreamIDs = append(sentryUpstreamIDs, parts[0])
		}

		// Convert upstream node IDs to lowercase (like other IDs) since
		// Tendermint stores them in a map and uses a case sensitive string
		// comparison to check ID equality.
		sentryUpstreamIDsStr := strings.ToLower(strings.Join(sentryUpstreamIDs, ","))
		tenderConfig.P2P.PrivatePeerIDs += "," + sentryUpstreamIDsStr
		tenderConfig.P2P.UnconditionalPeerIDs += "," + sentryUpstreamIDsStr
	}

	if !tenderConfig.P2P.PexReactor {
		t.Logger.Info("pex reactor disabled",
			logging.LogEvent, api.LogEventPeerExchangeDisabled,
		)
	}

	tendermintPV, err := crypto.LoadOrGeneratePrivVal(tendermintDataDir, t.identity.ConsensusSigner)
	if err != nil {
		return err
	}

	tmGenDoc, err := api.GetTendermintGenesisDocument(t.genesisProvider)
	if err != nil {
		t.Logger.Error("failed to obtain genesis document",
			"err", err,
		)
		return err
	}
	tendermintGenesisProvider := func() (*tmtypes.GenesisDoc, error) {
		return tmGenDoc, nil
	}

	dbProvider, err := db.GetProvider()
	if err != nil {
		t.Logger.Error("failed to obtain database provider",
			"err", err,
		)
		return err
	}

	// HACK: Wrap the provider so we can extract the state database handle. This is required because
	// Tendermint does not expose a way to access the state database and we need it to bypass some
	// stupid things like pagination on the in-process "client".
	wrapDbProvider := func(dbCtx *tmnode.DBContext) (tmdb.DB, error) {
		db, derr := dbProvider(dbCtx)
		if derr != nil {
			return nil, derr
		}

		switch dbCtx.ID {
		case "state":
			// Tendermint state database.
			t.stateStore = tmstate.NewStore(db)
		default:
		}

		return db, nil
	}

	// Configure state sync if enabled.
	var stateProvider tmstatesync.StateProvider
	if viper.GetBool(CfgConsensusStateSyncEnabled) {
		t.Logger.Info("state sync enabled")

		// Enable state sync in the configuration.
		tenderConfig.StateSync.Enable = true
		tenderConfig.StateSync.TrustHash = viper.GetString(CfgConsensusStateSyncTrustHash)

		// Create new state sync state provider.
		cfg := light.ClientConfig{
			GenesisDocument: tmGenDoc,
			TrustOptions: tmlight.TrustOptions{
				Period: viper.GetDuration(CfgConsensusStateSyncTrustPeriod),
				Height: int64(viper.GetUint64(CfgConsensusStateSyncTrustHeight)),
				Hash:   tenderConfig.StateSync.TrustHashBytes(),
			},
		}
		for _, rawAddr := range viper.GetStringSlice(CfgConsensusStateSyncConsensusNode) {
			var addr node.TLSAddress
			if err = addr.UnmarshalText([]byte(rawAddr)); err != nil {
				return fmt.Errorf("failed to parse state sync consensus node address (%s): %w", rawAddr, err)
			}

			cfg.ConsensusNodes = append(cfg.ConsensusNodes, addr)
		}
		if stateProvider, err = newStateProvider(t.ctx, cfg); err != nil {
			t.Logger.Error("failed to create state sync state provider",
				"err", err,
			)
			return fmt.Errorf("failed to create state sync state provider: %w", err)
		}
	}

	// HACK: tmnode.NewNode() triggers block replay and or ABCI chain
	// initialization, instead of t.node.Start().  This is a problem
	// because at the time that lazyInit() is called, none of the ABCI
	// applications are registered.
	//
	// Defer actually initializing the node till after everything
	// else is setup.
	t.startFn = func() (err error) {
		defer func() {
			// The node constructor can panic early in case an error occurrs during block replay as
			// the fail monitor is not yet initialized in that case. Propagate the error.
			if p := recover(); p != nil {
				switch pt := p.(type) {
				case error:
					err = pt
				default:
					err = fmt.Errorf("%v", pt)
				}
			}
		}()

		t.node, err = tmnode.NewNode(tenderConfig,
			tendermintPV,
			&tmp2p.NodeKey{PrivKey: crypto.SignerToTendermint(t.identity.P2PSigner)},
			tmproxy.NewLocalClientCreator(t.mux.Mux()),
			tendermintGenesisProvider,
			wrapDbProvider,
			tmnode.DefaultMetricsProvider(tenderConfig.Instrumentation),
			tmcommon.NewLogAdapter(!viper.GetBool(tmcommon.CfgLogDebug)),
			tmnode.StateProvider(stateProvider),
		)
		if err != nil {
			return fmt.Errorf("tendermint: failed to create node: %w", err)
		}
		if t.stateStore == nil {
			// Sanity check for the above wrapDbProvider hack in case the DB provider changes.
			return fmt.Errorf("tendermint: internal error: state database not set")
		}
		t.client = tmcli.New(t.node)
		t.failMonitor = newFailMonitor(t.ctx, t.Logger, t.node.ConsensusState().Wait)

		return nil
	}

	t.isInitialized = true

	return nil
}

func (t *fullService) syncWorker() {
	checkSyncFn := func() (isSyncing bool, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("tendermint: node disappeared, terminated?")
			}
		}()

		return t.node.ConsensusReactor().WaitSync(), nil
	}

	for {
		select {
		case <-t.node.Quit():
			return
		case <-time.After(1 * time.Second):
			isFastSyncing, err := checkSyncFn()
			if err != nil {
				t.Logger.Error("Failed to poll FastSync",
					"err", err,
				)
				return
			}
			if !isFastSyncing {
				t.Logger.Info("Tendermint Node finished fast-sync")

				// Check latest block time.
				tmBlock, err := t.GetTendermintBlock(t.ctx, consensusAPI.HeightLatest)
				if err != nil {
					t.Logger.Error("Failed to get tendermint block",
						"err", err,
					)
					return
				}

				now := time.Now()
				// No committed blocks or latest block within threshold.
				if tmBlock == nil || now.Sub(tmBlock.Header.Time) < syncWorkerLastBlockTimeDiffThreshold {
					t.Logger.Info("Tendermint Node finished initial sync")
					close(t.syncedCh)
					return
				}

				t.Logger.Debug("Node still syncing",
					"currentTime", now,
					"latestBlockTime", tmBlock.Time,
					"diff", now.Sub(tmBlock.Time),
				)
			}
		}
	}
}

func (t *fullService) blockNotifierWorker() {
	sub, err := t.node.EventBus().SubscribeUnbuffered(t.ctx, tmSubscriberID, tmtypes.EventQueryNewBlock)
	if err != nil {
		t.Logger.Error("failed to subscribe to new block events",
			"err", err,
		)
		return
	}
	// Oh yes, this can actually return a nil subscription even though the error was also
	// nil if the node is just shutting down.
	if sub == (*tmpubsub.Subscription)(nil) {
		return
	}
	defer t.node.EventBus().Unsubscribe(t.ctx, tmSubscriberID, tmtypes.EventQueryNewBlock) // nolint: errcheck

	for {
		select {
		// Should not return on t.ctx.Done()/t.node.Quit() as that could lead to a deadlock.
		case <-sub.Cancelled():
			return
		case v := <-sub.Out():
			ev := v.Data().(tmtypes.EventDataNewBlock)
			t.blockNotifier.Broadcast(ev.Block)
		}
	}
}

// metrics updates oasis_consensus metrics by checking last accepted block info.
func (t *fullService) metrics() {
	ch, sub := t.WatchTendermintBlocks()
	defer sub.Close()

	// Tendermint uses specific public key encoding.
	pubKey := t.identity.ConsensusSigner.Public()
	myAddr := []byte(crypto.PublicKeyToTendermint(&pubKey).Address())
	for {
		var blk *tmtypes.Block
		select {
		case <-t.node.Quit():
			return
		case blk = <-ch:
		}

		// Was block proposed by our node.
		if bytes.Equal(myAddr, blk.ProposerAddress) {
			metrics.ProposedBlocks.With(labelTendermint).Inc()
		}

		// Was block voted for by our node. Ignore if there was no previous block.
		if blk.LastCommit != nil {
			for _, sig := range blk.LastCommit.Signatures {
				if sig.Absent() || sig.BlockIDFlag == tmtypes.BlockIDFlagNil {
					// Vote is missing, ignore.
					continue
				}

				if bytes.Equal(myAddr, sig.ValidatorAddress) {
					metrics.SignedBlocks.With(labelTendermint).Inc()
					break
				}
			}
		}
	}
}

// New creates a new Tendermint consensus backend.
func New(
	ctx context.Context,
	dataDir string,
	identity *identity.Identity,
	upgrader upgradeAPI.Backend,
	genesisProvider genesisAPI.Provider,
) (consensusAPI.Backend, error) {
	// Retrieve the genesis document early so that it is possible to
	// use it while initializing other things.
	genesisDoc, err := genesisProvider.GetGenesisDocument()
	if err != nil {
		return nil, fmt.Errorf("tendermint: failed to get genesis doc: %w", err)
	}

	// Make sure that the consensus backend specified in the genesis
	// document is the correct one.
	if genesisDoc.Consensus.Backend != api.BackendName {
		return nil, fmt.Errorf("tendermint: genesis document contains incorrect consensus backend: %s",
			genesisDoc.Consensus.Backend,
		)
	}

	t := &fullService{
		BaseBackgroundService: *cmservice.NewBaseBackgroundService("tendermint"),
		svcMgr:                cmbackground.NewServiceManager(logging.GetLogger("tendermint/servicemanager")),
		upgrader:              upgrader,
		blockNotifier:         pubsub.NewBroker(false),
		identity:              identity,
		genesis:               genesisDoc,
		genesisProvider:       genesisProvider,
		ctx:                   ctx,
		dataDir:               dataDir,
		startedCh:             make(chan struct{}),
		syncedCh:              make(chan struct{}),
	}

	t.Logger.Info("starting a full consensus node")

	// Create the submission manager.
	pd, err := consensusAPI.NewStaticPriceDiscovery(viper.GetUint64(tmcommon.CfgSubmissionGasPrice))
	if err != nil {
		return nil, fmt.Errorf("tendermint: failed to create submission manager: %w", err)
	}
	t.submissionMgr = consensusAPI.NewSubmissionManager(t, pd, viper.GetUint64(tmcommon.CfgSubmissionMaxFee))

	return t, t.initialize()
}

func init() {
	Flags.String(CfgABCIPruneStrategy, abci.PruneDefault, "ABCI state pruning strategy")
	Flags.Uint64(CfgABCIPruneNumKept, 3600, "ABCI state versions kept (when applicable)")
	Flags.Bool(CfgCheckpointerDisabled, false, "Disable the ABCI state checkpointer")
	Flags.Duration(CfgCheckpointerCheckInterval, 1*time.Minute, "ABCI state checkpointer check interval")
	Flags.StringSlice(CfgSentryUpstreamAddress, []string{}, "Tendermint nodes for which we act as sentry of the form ID@ip:port")
	Flags.StringSlice(CfgP2PPersistentPeer, []string{}, "Tendermint persistent peer(s) of the form ID@ip:port")
	Flags.StringSlice(CfgP2PUnconditionalPeerIDs, []string{}, "Tendermint unconditional peer IDs")
	Flags.Bool(CfgP2PDisablePeerExchange, false, "Disable Tendermint's peer-exchange reactor")
	Flags.Duration(CfgP2PPersistenPeersMaxDialPeriod, 0*time.Second, "Tendermint max timeout when redialing a persistent peer (default: unlimited)")
	Flags.Uint64(CfgMinGasPrice, 0, "minimum gas price")
	Flags.Bool(CfgDebugDisableCheckTx, false, "do not perform CheckTx on incoming transactions (UNSAFE)")
	Flags.Bool(CfgDebugUnsafeReplayRecoverCorruptedWAL, false, "Enable automatic recovery from corrupted WAL during replay (UNSAFE).")

	Flags.Bool(CfgSupplementarySanityEnabled, false, "enable supplementary sanity checks (slows down consensus)")
	Flags.Uint64(CfgSupplementarySanityInterval, 10, "supplementary sanity check interval (in blocks)")

	// State sync.
	Flags.Bool(CfgConsensusStateSyncEnabled, false, "enable state sync")
	Flags.StringSlice(CfgConsensusStateSyncConsensusNode, []string{}, "state sync: consensus node to use for syncing the light client")
	Flags.Duration(CfgConsensusStateSyncTrustPeriod, 24*time.Hour, "state sync: light client trust period")
	Flags.Uint64(CfgConsensusStateSyncTrustHeight, 0, "state sync: light client trusted height")
	Flags.String(CfgConsensusStateSyncTrustHash, "", "state sync: light client trusted consensus header hash")

	_ = Flags.MarkHidden(CfgDebugDisableCheckTx)
	_ = Flags.MarkHidden(CfgDebugUnsafeReplayRecoverCorruptedWAL)

	_ = Flags.MarkHidden(CfgSupplementarySanityEnabled)
	_ = Flags.MarkHidden(CfgSupplementarySanityInterval)

	_ = viper.BindPFlags(Flags)
	Flags.AddFlagSet(db.Flags)
}
