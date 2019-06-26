// Package badger implements the BadgeDB backed storage backend.
package badger

import (
	"context"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger"
	"github.com/pkg/errors"

	"github.com/oasislabs/ekiden/go/common/crypto/hash"
	"github.com/oasislabs/ekiden/go/common/crypto/signature"
	"github.com/oasislabs/ekiden/go/common/logging"
	"github.com/oasislabs/ekiden/go/storage/api"
	nodedb "github.com/oasislabs/ekiden/go/storage/mkvs/urkel/db/api"
	badgerNodedb "github.com/oasislabs/ekiden/go/storage/mkvs/urkel/db/badger"
)

const (
	// BackendName is the name of this implementation.
	BackendName = "badger"

	// DBFile is the default backing store filename.
	DBFile = "mkvs_storage.badger.db"
)

type badgerBackend struct {
	nodedb    nodedb.NodeDB
	rootCache *api.RootCache

	signingKey *signature.PrivateKey
	initCh     chan struct{}
}

// New constructs a new Badger backed storage Backend instance.
func New(dbDir string, signingKey *signature.PrivateKey, lruSizeInBytes, applyLockLRUSlots uint64) (api.Backend, error) {
	logger := logging.GetLogger("storage/badger")

	opts := badger.DefaultOptions(dbDir)
	opts = opts.WithLogger(NewLogAdapter(logger))

	ndb, err := badgerNodedb.New(opts)
	if err != nil {
		return nil, errors.Wrap(err, "storage/badger: failed to open node database")
	}

	rootCache, err := api.NewRootCache(ndb, lruSizeInBytes, applyLockLRUSlots)
	if err != nil {
		ndb.Close()
		return nil, errors.Wrap(err, "storage/badger: failed to create root cache")
	}

	// Satisfy the interface....
	initCh := make(chan struct{})
	close(initCh)

	return &badgerBackend{
		nodedb:     ndb,
		rootCache:  rootCache,
		signingKey: signingKey,
		initCh:     initCh,
	}, nil
}

func (ba *badgerBackend) Apply(ctx context.Context, root, expectedNewRoot hash.Hash, log api.WriteLog) ([]*api.Receipt, error) {
	newRoot, err := ba.rootCache.Apply(ctx, root, expectedNewRoot, log)
	if err != nil {
		return nil, errors.Wrap(err, "storage/badger: failed to Apply")
	}

	receipt, err := ba.signReceipt(ctx, []hash.Hash{*newRoot})
	return []*api.Receipt{receipt}, err
}

func (ba *badgerBackend) ApplyBatch(ctx context.Context, ops []api.ApplyOp) ([]*api.Receipt, error) {
	newRoots := make([]hash.Hash, 0, len(ops))
	for _, op := range ops {
		newRoot, err := ba.rootCache.Apply(ctx, op.Root, op.ExpectedNewRoot, op.WriteLog)
		if err != nil {
			return nil, errors.Wrap(err, "storage/badger: failed to Apply, op")
		}
		newRoots = append(newRoots, *newRoot)
	}

	receipt, err := ba.signReceipt(ctx, newRoots)
	return []*api.Receipt{receipt}, err
}

func (ba *badgerBackend) Cleanup() {
	ba.nodedb.Close()
}

func (ba *badgerBackend) Initialized() <-chan struct{} {
	return ba.initCh
}

func (ba *badgerBackend) GetSubtree(ctx context.Context, root hash.Hash, id api.NodeID, maxDepth uint8) (*api.Subtree, error) {
	tree, err := ba.rootCache.GetTree(ctx, root)
	if err != nil {
		return nil, err
	}

	return tree.GetSubtree(ctx, root, id, maxDepth)
}

func (ba *badgerBackend) GetPath(ctx context.Context, root, key hash.Hash, startDepth uint8) (*api.Subtree, error) {
	tree, err := ba.rootCache.GetTree(ctx, root)
	if err != nil {
		return nil, err
	}

	return tree.GetPath(ctx, root, key, startDepth)
}

func (ba *badgerBackend) GetNode(ctx context.Context, root hash.Hash, id api.NodeID) (api.Node, error) {
	tree, err := ba.rootCache.GetTree(ctx, root)
	if err != nil {
		return nil, err
	}

	return tree.GetNode(ctx, root, id)
}

func (ba *badgerBackend) GetValue(ctx context.Context, root hash.Hash, id hash.Hash) ([]byte, error) {
	tree, err := ba.rootCache.GetTree(ctx, root)
	if err != nil {
		return nil, err
	}

	return tree.GetValue(ctx, root, id)
}

func (ba *badgerBackend) signReceipt(ctx context.Context, roots []hash.Hash) (*api.Receipt, error) {
	receiptBody := api.ReceiptBody{
		Version: 1,
		Roots:   roots,
	}
	signed, err := signature.SignSigned(*ba.signingKey, api.ReceiptSignatureContext, &receiptBody)
	if err != nil {
		return nil, errors.Wrap(err, "storage/badger: failed to sign receipt")
	}

	return &api.Receipt{
		Signed: *signed,
	}, nil
}

// NewLogAdapter returns a badger.Logger backed by an ekiden logger.
func NewLogAdapter(logger *logging.Logger) badger.Logger {
	return &badgerLogger{
		logger: logger,
	}
}

type badgerLogger struct {
	logger *logging.Logger
}

func (l *badgerLogger) Errorf(format string, a ...interface{}) {
	l.logger.Error(strings.TrimSpace(fmt.Sprintf(format, a...)))
}

func (l *badgerLogger) Warningf(format string, a ...interface{}) {
	l.logger.Warn(strings.TrimSpace(fmt.Sprintf(format, a...)))
}

func (l *badgerLogger) Infof(format string, a ...interface{}) {
	l.logger.Info(strings.TrimSpace(fmt.Sprintf(format, a...)))
}

func (l *badgerLogger) Debugf(format string, a ...interface{}) {
	l.logger.Debug(strings.TrimSpace(fmt.Sprintf(format, a...)))
}
