// Package storage implements the storage sub-commands.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/oasisprotocol/oasis-core/go/common/crypto/hash"
	"github.com/oasisprotocol/oasis-core/go/common/logging"
	cmdCommon "github.com/oasisprotocol/oasis-core/go/oasis-node/cmd/common"
	"github.com/oasisprotocol/oasis-core/go/runtime/history"
	"github.com/oasisprotocol/oasis-core/go/runtime/registry"
	db "github.com/oasisprotocol/oasis-core/go/storage/mkvs/db/api"
	"github.com/oasisprotocol/oasis-core/go/storage/mkvs/db/badger"
	"github.com/oasisprotocol/oasis-core/go/storage/mkvs/node"
)

var (
	storageCmd = &cobra.Command{
		Use:   "storage",
		Short: "storage node utilities",
	}

	storageMigrateCmd = &cobra.Command{
		Use:   "migrate",
		Short: "perform node database migration",
		Run:   doMigrate,
	}

	logger = logging.GetLogger("cmd/storage")

	pretty = cmdCommon.Isatty(1)
)

type migrateHelper struct {
	ctx     context.Context
	history history.History
	roots   map[hash.Hash]node.RootType

	lastTime time.Time
	lastStatus string
}

func (mh *migrateHelper) GetRootForHash(root hash.Hash, version uint64) (node.Root, error) {
	block, err := mh.history.GetBlock(mh.ctx, version)
	if err != nil {
		//return node.Root{}, err
		// XXX
		return node.Root{
			Namespace: mh.history.RuntimeID(),
			Version: version,
			Type: node.RootTypeInvalid,
			Hash: root,
		}, nil
	}

	for _, blockRoot := range block.Header.StorageRoots() {
		if blockRoot.Hash.Equal(&root) {
			return blockRoot, nil
		}
	}
	return node.Root{}, fmt.Errorf("root %v:%v not found", root, version)
}

func (mh *migrateHelper) ReportStatus(msg string) {
	mh.lastTime = time.Time{}
	if pretty {
		fmt.Printf("\n- %s...\033[K\r", msg)
	} else {
		logger.Info(msg)
	}
	mh.lastStatus = msg
}

func (mh *migrateHelper) ReportProgress(msg string, current, total uint64) {
	if pretty {
		if time.Since(mh.lastTime).Seconds() < 0.1 {
			return
		}
		mh.lastTime = time.Now()

		var leadin string
		if len(mh.lastStatus) > 0 {
			leadin = fmt.Sprintf("- %s:", mh.lastStatus)
		} else {
			leadin = "-"
		}
		fmt.Printf("%s %s %.2f%% (%d / %d)\033[K\r", leadin, msg, (float64(current)/float64(total))*100.0, current, total)
	}
}

func doMigrate(cmd *cobra.Command, args []string) {
	dataDir := cmdCommon.DataDir()
	ctx := context.Background()

	runtimes, err := registry.ParseRuntimeMap(viper.GetStringSlice(registry.CfgSupported))
	if err != nil {
		logger.Error("unable to enumerate configured runtimes", "err", err)
		return
	}

	for rt := range runtimes {
		if pretty {
			fmt.Printf(" ** Upgrading storage database for runtime %v...\r", rt)
		}
		err := func() error {
			history, err := history.New(dataDir, rt, nil)
			if err != nil {
				return fmt.Errorf("error creating history provider: %w", err)
			}
			defer history.Close()

			nodeCfg := &db.Config{
				DB:        dataDir,
				Namespace: rt,
			}

			helper := &migrateHelper{
				ctx:     ctx,
				history: history,
				roots:   map[hash.Hash]node.RootType{},
			}

			newVersion, err := badger.Migrate(nodeCfg, helper)
			if err != nil {
				return fmt.Errorf("node datagase migrator returned error: %w", err)
			}
			if !pretty {
				logger.Info("successfully migrated node database", "new_version", newVersion)
			}
			return nil
		}()
		if err != nil {
			logger.Error("error upgrading runtime", "rt", rt, "err", err)
			if pretty {
				fmt.Printf("\nerror upgrading runtime %v: %v\n", rt, err)
			}
			return
		} else {
			if pretty {
				fmt.Printf("\n")
			}
		}
	}
}

// Register registers the client sub-command and all of its children.
func Register(parentCmd *cobra.Command) {
	storageMigrateCmd.Flags().AddFlagSet(registry.Flags)
	storageCmd.AddCommand(storageMigrateCmd)
	parentCmd.AddCommand(storageCmd)
}
