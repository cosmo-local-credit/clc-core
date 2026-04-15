package worker

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/protocol/pkg/publish"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/giftabletoken"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
)

const upgradeGasLimit = 150_000

var (
	funcDeployAndCall  = w3.MustNewFunc("deployAndCall(address,address,bytes)", "address")
	funcUpgrade        = w3.MustNewFunc("upgrade(address,address)", "")
	funcUpgradeAndCall = w3.MustNewFunc("upgradeAndCall(address,address,bytes)", "")
)

type (
	DeployERC20Args struct {
		TrackingID string `json:"trackingId"`
	}

	UpgradeERC20Args struct {
		TrackingID string `json:"trackingId"`
	}

	DeployERC20Worker struct {
		river.WorkerDefaults[DeployERC20Args]
		wc *WorkerContainer
	}

	UpgradeERC20Worker struct {
		river.WorkerDefaults[UpgradeERC20Args]
		wc *WorkerContainer
	}

	preparedTx struct {
		OTXID  uint64
		Hash   string
		RawHex string
	}
)

func (DeployERC20Args) Kind() string  { return "NEXT_ERC20_DEPLOY" }
func (UpgradeERC20Args) Kind() string { return "NEXT_ERC20_UPGRADE" }

func (w *DeployERC20Worker) Work(ctx context.Context, job *river.Job[DeployERC20Args]) (workErr error) {
	workflow, err := w.wc.loadWorkflow(ctx, job.Args.TrackingID)
	if err != nil {
		return err
	}

	defer func() {
		if workErr != nil {
			w.wc.markWorkflowFailed(context.Background(), workflow.TrackingID)
		}
	}()

	privateKey, signerAddress, err := w.wc.loadMasterSigner(ctx)
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	implementationAddress, implementationTx, err := w.wc.preparePublishTx(
		ctx,
		workflow.TrackingID,
		store.NEXT_ERC20_IMPLEMENTATION_DEPLOY,
		signerAddress,
		privateKey,
		giftabletoken.Bytecode(),
		giftabletoken.ImplGasLimit,
		gasSettings.GasFeeCap,
		gasSettings.GasTipCap,
	)
	if err != nil {
		return err
	}

	initData, err := giftabletoken.EncodeInit(giftabletoken.InitArgs{
		Name:      workflow.Name,
		Symbol:    workflow.Symbol,
		Decimals:  uint8(workflow.Decimals),
		Owner:     common.HexToAddress(workflow.Owner),
		ExpiresAt: big.NewInt(workflow.ExpiryTimestamp),
	})
	if err != nil {
		return err
	}

	proxyCalldata, err := funcDeployAndCall.EncodeArgs(
		implementationAddress,
		common.HexToAddress(workflow.AdminAddress),
		initData,
	)
	if err != nil {
		return err
	}

	proxyTx, err := w.wc.prepareContractExecutionTx(
		ctx,
		workflow.TrackingID,
		store.NEXT_ERC20_PROXY_DEPLOY,
		signerAddress,
		privateKey,
		common.HexToAddress(workflow.FactoryAddress),
		proxyCalldata,
		publish.ProxyGasLimit,
		gasSettings.GasFeeCap,
		gasSettings.GasTipCap,
	)
	if err != nil {
		return err
	}

	// enqueue dispatch jobs for implementation and proxy deployment
	var insertParams []river.InsertManyParams
	if implementationTx != nil {
		insertParams = append(insertParams, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: workflow.TrackingID,
				OTXID:      implementationTx.OTXID,
				RawTx:      implementationTx.RawHex,
			},
			InsertOpts: &river.InsertOpts{Priority: 1},
		})
	}
	if proxyTx != nil {
		insertParams = append(insertParams, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: workflow.TrackingID,
				OTXID:      proxyTx.OTXID,
				RawTx:      proxyTx.RawHex,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		})
	}

	if len(insertParams) > 0 {
		if _, err := w.wc.queueClient.InsertMany(ctx, insertParams); err != nil {
			return err
		}
	}

	var implTxHash string
	if implementationTx != nil {
		implTxHash = implementationTx.Hash
	}

	if err := w.wc.updateWorkflow(ctx, store.NextERC20WorkflowUpdate{
		TrackingID:            workflow.TrackingID,
		Status:                store.NextERC20StatusRunning,
		ImplementationAddress: implementationAddress.Hex(),
		ImplementationTxHash:  implTxHash,
		ActionTxHash:          proxyTx.Hash,
	}); err != nil {
		return err
	}

	return nil
}

func (w *UpgradeERC20Worker) Work(ctx context.Context, job *river.Job[UpgradeERC20Args]) (workErr error) {
	workflow, err := w.wc.loadWorkflow(ctx, job.Args.TrackingID)
	if err != nil {
		return err
	}

	defer func() {
		if workErr != nil {
			w.wc.markWorkflowFailed(context.Background(), workflow.TrackingID)
		}
	}()

	privateKey, signerAddress, err := w.wc.loadMasterSigner(ctx)
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	implementationAddress, implementationTx, err := w.wc.preparePublishTx(
		ctx,
		workflow.TrackingID,
		store.NEXT_ERC20_IMPLEMENTATION_DEPLOY,
		signerAddress,
		privateKey,
		giftabletoken.Bytecode(),
		giftabletoken.ImplGasLimit,
		gasSettings.GasFeeCap,
		gasSettings.GasTipCap,
	)
	if err != nil {
		return err
	}

	var actionData []byte
	if strings.TrimSpace(workflow.UpgradeCalldata) != "" {
		upgradeCalldata, err := decodeHexData(workflow.UpgradeCalldata)
		if err != nil {
			return err
		}
		actionData, err = funcUpgradeAndCall.EncodeArgs(common.HexToAddress(workflow.ProxyAddress), implementationAddress, upgradeCalldata)
		if err != nil {
			return err
		}
	} else {
		actionData, err = funcUpgrade.EncodeArgs(common.HexToAddress(workflow.ProxyAddress), implementationAddress)
		if err != nil {
			return err
		}
	}

	upgradeTx, err := w.wc.prepareContractExecutionTx(
		ctx,
		workflow.TrackingID,
		store.NEXT_ERC20_PROXY_UPGRADE,
		signerAddress,
		privateKey,
		common.HexToAddress(workflow.FactoryAddress),
		actionData,
		upgradeGasLimit,
		gasSettings.GasFeeCap,
		gasSettings.GasTipCap,
	)
	if err != nil {
		return err
	}

	// enqueue dispatch jobs
	var insertParamsUpgrade []river.InsertManyParams
	if implementationTx != nil {
		insertParamsUpgrade = append(insertParamsUpgrade, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: workflow.TrackingID,
				OTXID:      implementationTx.OTXID,
				RawTx:      implementationTx.RawHex,
			},
			InsertOpts: &river.InsertOpts{Priority: 1},
		})
	}
	if upgradeTx != nil {
		insertParamsUpgrade = append(insertParamsUpgrade, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: workflow.TrackingID,
				OTXID:      upgradeTx.OTXID,
				RawTx:      upgradeTx.RawHex,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		})
	}

	if len(insertParamsUpgrade) > 0 {
		if _, err := w.wc.queueClient.InsertMany(ctx, insertParamsUpgrade); err != nil {
			return err
		}
	}

	var implTxHashUp string
	if implementationTx != nil {
		implTxHashUp = implementationTx.Hash
	}

	if err := w.wc.updateWorkflow(ctx, store.NextERC20WorkflowUpdate{
		TrackingID:                    workflow.TrackingID,
		Status:                        store.NextERC20StatusRunning,
		ImplementationAddress:         implementationAddress.Hex(),
		PreviousImplementationAddress: workflow.PreviousImplementationAddress,
		ImplementationTxHash:          implTxHashUp,
		ActionTxHash:                  upgradeTx.Hash,
	}); err != nil {
		return err
	}

	return nil
}

func (w *WorkerContainer) loadWorkflow(ctx context.Context, trackingID string) (store.NextERC20Workflow, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return store.NextERC20Workflow{}, err
	}
	defer tx.Rollback(ctx)

	workflow, err := w.store.GetNextERC20WorkflowByTrackingID(ctx, tx, trackingID)
	if err != nil {
		return store.NextERC20Workflow{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return store.NextERC20Workflow{}, err
	}

	return workflow, nil
}

func (w *WorkerContainer) loadMasterSigner(ctx context.Context) (*ecdsa.PrivateKey, common.Address, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return nil, common.Address{}, err
	}
	defer tx.Rollback(ctx)

	systemKeypair, err := w.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return nil, common.Address{}, err
	}

	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return nil, common.Address{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, common.Address{}, err
	}

	return privateKey, common.HexToAddress(systemKeypair.Public), nil
}

func (w *WorkerContainer) preparePublishTx(
	ctx context.Context,
	trackingID string,
	otxType string,
	signerAddress common.Address,
	privateKey *ecdsa.PrivateKey,
	bytecode []byte,
	gasLimit uint64,
	gasFeeCap *big.Int,
	gasTipCap *big.Int,
) (common.Address, *preparedTx, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return common.Address{}, nil, err
	}
	defer tx.Rollback(ctx)

	nonce, err := w.store.AcquireNonce(ctx, tx, signerAddress.Hex())
	if err != nil {
		return common.Address{}, nil, err
	}

	builtTx, err := w.chainProvider.SignContractPublishTx(privateKey, ethutils.ContractPublishTxOpts{
		ContractByteCode: bytecode,
		GasFeeCap:        gasFeeCap,
		GasTipCap:        gasTipCap,
		GasLimit:         gasLimit,
		Nonce:            nonce,
	})
	if err != nil {
		return common.Address{}, nil, err
	}

	prepared, err := w.insertTrackedTx(ctx, tx, trackingID, otxType, signerAddress.Hex(), nonce, builtTx)
	if err != nil {
		return common.Address{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return common.Address{}, nil, err
	}

	return crypto.CreateAddress(signerAddress, nonce), prepared, nil
}

func (w *WorkerContainer) prepareContractExecutionTx(
	ctx context.Context,
	trackingID string,
	otxType string,
	signerAddress common.Address,
	privateKey *ecdsa.PrivateKey,
	contractAddress common.Address,
	inputData []byte,
	gasLimit uint64,
	gasFeeCap *big.Int,
	gasTipCap *big.Int,
) (*preparedTx, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	nonce, err := w.store.AcquireNonce(ctx, tx, signerAddress.Hex())
	if err != nil {
		return nil, err
	}

	builtTx, err := w.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: contractAddress,
		InputData:       inputData,
		GasFeeCap:       gasFeeCap,
		GasTipCap:       gasTipCap,
		GasLimit:        gasLimit,
		Nonce:           nonce,
	})
	if err != nil {
		return nil, err
	}

	prepared, err := w.insertTrackedTx(ctx, tx, trackingID, otxType, signerAddress.Hex(), nonce, builtTx)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return prepared, nil
}

func (w *WorkerContainer) insertTrackedTx(ctx context.Context, tx pgx.Tx, trackingID string, otxType string, signerAccount string, nonce uint64, builtTx *types.Transaction) (*preparedTx, error) {
	rawTx, err := builtTx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	rawTxHex := hexutil.Encode(rawTx)
	otxID, err := w.store.InsertOTX(ctx, tx, store.OTX{
		TrackingID:    trackingID,
		OTXType:       otxType,
		SignerAccount: signerAccount,
		RawTx:         rawTxHex,
		TxHash:        builtTx.Hash().Hex(),
		Nonce:         nonce,
	})
	if err != nil {
		return nil, err
	}

	if err := w.store.InsertDispatchTx(ctx, tx, store.DispatchTx{OTXID: otxID, Status: store.PENDING}); err != nil {
		return nil, err
	}

	return &preparedTx{
		OTXID:  otxID,
		Hash:   builtTx.Hash().Hex(),
		RawHex: rawTxHex,
	}, nil
}

func (w *WorkerContainer) updateWorkflow(ctx context.Context, patch store.NextERC20WorkflowUpdate) error {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := w.store.UpdateNextERC20Workflow(ctx, tx, patch); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (w *WorkerContainer) markWorkflowFailed(ctx context.Context, trackingID string) {
	if err := w.updateWorkflow(ctx, store.NextERC20WorkflowUpdate{
		TrackingID: trackingID,
		Status:     store.NextERC20StatusFailed,
	}); err != nil {
		w.logg.Error("failed to mark next workflow as failed", "tracking_id", trackingID, "err", err)
	}
}

func decodeHexData(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if !strings.HasPrefix(trimmed, "0x") {
		trimmed = "0x" + trimmed
	}

	return hexutil.Decode(trimmed)
}
