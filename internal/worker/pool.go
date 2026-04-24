package worker

import (
	"context"
	"math/big"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/protocol/pkg/publish"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/feepolicy"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/limiter"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/oraclequoter"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/relativequoter"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/swappool"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/tokenuniquesymbolindex"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

type (
	DeployPoolArgs struct {
		TrackingID                 string `json:"trackingId"`
		PoolName                   string `json:"poolName"`
		PoolSymbol                 string `json:"poolSymbol"`
		PoolDecimals               string `json:"poolDecimals"`
		Owner                      string `json:"owner"`
		QuoterType                 string `json:"quoterType"`
		FeesDecoupled              bool   `json:"feesDecoupled"`
		FeePolicyDefaultFee        string `json:"feePolicyDefaultFee"`
		BaseCurrency               string `json:"baseCurrency"`
		FactoryAddress             string `json:"factoryAddress"`
		SwapPoolImplAddress        string `json:"swapPoolImplAddress"`
		TokenIndexImplAddress      string `json:"tokenIndexImplAddress"`
		LimiterImplAddress         string `json:"limiterImplAddress"`
		FeePolicyImplAddress       string `json:"feePolicyImplAddress"`
		QuoterImplAddress          string `json:"quoterImplAddress"`
		DecimalQuoterAddress       string `json:"decimalQuoterAddress"`
		ProtocolFeeController      string `json:"protocolFeeController"`
		SwapPoolPlatformContractID uint64 `json:"swapPoolPlatformContractId"`
	}

	DeployPoolWorker struct {
		river.WorkerDefaults[DeployPoolArgs]
		wc *WorkerContainer
	}
)

func (DeployPoolArgs) Kind() string { return "POOL_DEPLOY" }

func (w *DeployPoolWorker) Work(ctx context.Context, job *river.Job[DeployPoolArgs]) error {
	args := job.Args

	poolDecimals, err := strconv.ParseUint(args.PoolDecimals, 10, 8)
	if err != nil {
		return err
	}

	privateKey, signerAddress, err := w.wc.loadMasterSigner(ctx)
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	factoryAddress := common.HexToAddress(args.FactoryAddress)

	createSalt := func(suffix string) common.Hash {
		hash := crypto.Keccak256Hash([]byte(args.TrackingID + "-" + suffix))
		var salt common.Hash
		copy(salt[:20], signerAddress.Bytes())
		copy(salt[20:], hash[20:])
		return salt
	}

	funcPredict := w3.MustNewFunc("predictDeterministicAddress(bytes32)", "address")
	predictAddress := func(salt common.Hash) (common.Address, error) {
		var addr common.Address
		err := w.wc.chainProvider.Client.CallCtx(ctx, eth.CallFunc(factoryAddress, funcPredict, salt).Returns(&addr))
		return addr, err
	}

	needsQuoterProxy := args.QuoterType != "decimal"
	depCount := 3 // TokenIndex, Limiter, FeePolicy
	if needsQuoterProxy {
		depCount++
	}
	totalDeployments := depCount + 1 // +1 for SwapPool

	tokenIndexSalt := createSalt("TokenIndex")
	limiterSalt := createSalt("Limiter")
	feePolicySalt := createSalt("FeePolicy")
	swapPoolSalt := createSalt("SwapPool")

	tokenIndexAddr, err := predictAddress(tokenIndexSalt)
	if err != nil {
		return err
	}
	limiterAddr, err := predictAddress(limiterSalt)
	if err != nil {
		return err
	}
	feePolicyAddr, err := predictAddress(feePolicySalt)
	if err != nil {
		return err
	}

	var quoterSalt common.Hash
	var quoterAddr common.Address
	if needsQuoterProxy {
		quoterSalt = createSalt("Quoter")
		quoterAddr, err = predictAddress(quoterSalt)
		if err != nil {
			return err
		}
	} else {
		quoterAddr = common.HexToAddress(args.DecimalQuoterAddress)
	}

	// Begin DB transaction for all OTX inserts + dispatch queueing
	dbTx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	// Acquire consecutive nonces for all deployments
	signerNonces := make([]uint64, totalDeployments)
	for i := 0; i < totalDeployments; i++ {
		nonce, err := w.wc.store.AcquireNonce(ctx, dbTx, signerAddress.Hex())
		if err != nil {
			return err
		}
		signerNonces[i] = nonce
	}

	var dispatchParams []river.InsertManyParams

	ownerAddress := common.HexToAddress(args.Owner)

	// --- 1. TokenUniqueSymbolIndex ---
	tokenIndexInit, err := tokenuniquesymbolindex.EncodeInit(tokenuniquesymbolindex.InitArgs{
		Owner: ownerAddress,
	})
	if err != nil {
		return err
	}

	tokenIndexCalldata, err := funcDeployDeterministicAndCall.EncodeArgs(
		common.HexToAddress(args.TokenIndexImplAddress),
		signerAddress,
		tokenIndexSalt,
		tokenIndexInit,
	)
	if err != nil {
		return err
	}

	tokenIndexSignedTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: factoryAddress,
		InputData:       tokenIndexCalldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        publish.ProxyGasLimit,
		Nonce:           signerNonces[0],
	})
	if err != nil {
		return err
	}

	tokenIndexPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_DEPLOY_STEP, signerAddress.Hex(), signerNonces[0], tokenIndexSignedTx)
	if err != nil {
		return err
	}

	dispatchParams = append(dispatchParams, river.InsertManyParams{
		Args: DispatchArgs{
			TrackingID: args.TrackingID,
			OTXID:      tokenIndexPrepared.OTXID,
			RawTx:      tokenIndexPrepared.RawHex,
		},
		InsertOpts: &river.InsertOpts{Priority: 2},
	})

	// --- 2. Limiter ---
	limiterInit, err := limiter.EncodeInit(limiter.InitArgs{
		Owner: ownerAddress,
	})
	if err != nil {
		return err
	}

	limiterCalldata, err := funcDeployDeterministicAndCall.EncodeArgs(
		common.HexToAddress(args.LimiterImplAddress),
		signerAddress,
		limiterSalt,
		limiterInit,
	)
	if err != nil {
		return err
	}

	limiterSignedTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: factoryAddress,
		InputData:       limiterCalldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        publish.ProxyGasLimit,
		Nonce:           signerNonces[1],
	})
	if err != nil {
		return err
	}

	limiterPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_DEPLOY_STEP, signerAddress.Hex(), signerNonces[1], limiterSignedTx)
	if err != nil {
		return err
	}

	dispatchParams = append(dispatchParams, river.InsertManyParams{
		Args: DispatchArgs{
			TrackingID: args.TrackingID,
			OTXID:      limiterPrepared.OTXID,
			RawTx:      limiterPrepared.RawHex,
		},
		InsertOpts: &river.InsertOpts{Priority: 2},
	})

	// --- 3. FeePolicy ---
	var feePolicyDefaultFee uint64
	if args.FeePolicyDefaultFee != "" {
		feePolicyDefaultFee, err = strconv.ParseUint(args.FeePolicyDefaultFee, 10, 64)
		if err != nil {
			return err
		}
	}

	feePolicyInit, err := feepolicy.EncodeInit(feepolicy.InitArgs{
		Owner:      ownerAddress,
		DefaultFee: new(big.Int).SetUint64(feePolicyDefaultFee),
	})
	if err != nil {
		return err
	}

	feePolicyCalldata, err := funcDeployDeterministicAndCall.EncodeArgs(
		common.HexToAddress(args.FeePolicyImplAddress),
		signerAddress,
		feePolicySalt,
		feePolicyInit,
	)
	if err != nil {
		return err
	}

	feePolicySignedTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: factoryAddress,
		InputData:       feePolicyCalldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        publish.ProxyGasLimit,
		Nonce:           signerNonces[2],
	})
	if err != nil {
		return err
	}

	feePolicyPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_DEPLOY_STEP, signerAddress.Hex(), signerNonces[2], feePolicySignedTx)
	if err != nil {
		return err
	}

	dispatchParams = append(dispatchParams, river.InsertManyParams{
		Args: DispatchArgs{
			TrackingID: args.TrackingID,
			OTXID:      feePolicyPrepared.OTXID,
			RawTx:      feePolicyPrepared.RawHex,
		},
		InsertOpts: &river.InsertOpts{Priority: 2},
	})

	// --- 4. Quoter proxy (if oracle/relative) ---
	if needsQuoterProxy {
		var quoterInit []byte

		switch args.QuoterType {
		case "oracle":
			quoterInit, err = oraclequoter.EncodeInit(oraclequoter.InitArgs{
				Owner:        ownerAddress,
				BaseCurrency: common.HexToAddress(args.BaseCurrency),
			})
		case "relative":
			quoterInit, err = relativequoter.EncodeInit(relativequoter.InitArgs{
				Owner: ownerAddress,
			})
		}
		if err != nil {
			return err
		}

		quoterCalldata, err := funcDeployDeterministicAndCall.EncodeArgs(
			common.HexToAddress(args.QuoterImplAddress),
			signerAddress,
			quoterSalt,
			quoterInit,
		)
		if err != nil {
			return err
		}

		quoterSignedTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
			ContractAddress: factoryAddress,
			InputData:       quoterCalldata,
			GasFeeCap:       gasSettings.GasFeeCap,
			GasTipCap:       gasSettings.GasTipCap,
			GasLimit:        publish.ProxyGasLimit,
			Nonce:           signerNonces[3],
		})
		if err != nil {
			return err
		}

		quoterPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_DEPLOY_STEP, signerAddress.Hex(), signerNonces[3], quoterSignedTx)
		if err != nil {
			return err
		}

		dispatchParams = append(dispatchParams, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      quoterPrepared.OTXID,
				RawTx:      quoterPrepared.RawHex,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		})
	}

	// --- 5. SwapPool ---
	swapPoolInit, err := swappool.EncodeInit(swappool.InitArgs{
		Name:                  args.PoolName,
		Symbol:                args.PoolSymbol,
		Decimals:              uint8(poolDecimals),
		Owner:                 ownerAddress,
		FeePolicy:             feePolicyAddr,
		FeeAddress:            ownerAddress,
		TokenRegistry:         tokenIndexAddr,
		TokenLimiter:          limiterAddr,
		Quoter:                quoterAddr,
		FeesDecoupled:         args.FeesDecoupled,
		ProtocolFeeController: common.HexToAddress(args.ProtocolFeeController),
	})
	if err != nil {
		return err
	}

	swapPoolCalldata, err := funcDeployDeterministicAndCall.EncodeArgs(
		common.HexToAddress(args.SwapPoolImplAddress),
		signerAddress,
		swapPoolSalt,
		swapPoolInit,
	)
	if err != nil {
		return err
	}

	swapPoolNonceIdx := depCount
	swapPoolSignedTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: factoryAddress,
		InputData:       swapPoolCalldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        publish.ProxyGasLimit,
		Nonce:           signerNonces[swapPoolNonceIdx],
	})
	if err != nil {
		return err
	}

	swapPoolPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_PROXY_DEPLOY, signerAddress.Hex(), signerNonces[swapPoolNonceIdx], swapPoolSignedTx)
	if err != nil {
		return err
	}

	dispatchParams = append(dispatchParams, river.InsertManyParams{
		Args: DispatchArgs{
			TrackingID: args.TrackingID,
			OTXID:      swapPoolPrepared.OTXID,
			RawTx:      swapPoolPrepared.RawHex,
		},
		InsertOpts: &river.InsertOpts{Priority: 1},
	})

	// Insert pool deploy meta linked to the SwapPool OTX
	if err := w.wc.store.InsertPoolDeployMeta(ctx, dbTx, store.PoolDeployMeta{
		OTXID:                        swapPoolPrepared.OTXID,
		PlatformContractID:           args.SwapPoolPlatformContractID,
		PoolName:                     args.PoolName,
		PoolSymbol:                   args.PoolSymbol,
		PoolDecimals:                 uint8(poolDecimals),
		TokenRegistryAddress:         tokenIndexAddr.Hex(),
		LimiterAddress:               limiterAddr.Hex(),
		FeePolicyAddress:             feePolicyAddr.Hex(),
		QuoterAddress:                quoterAddr.Hex(),
		ProtocolFeeControllerAddress: args.ProtocolFeeController,
		FeesDecoupled:                args.FeesDecoupled,
		Admin:                        ownerAddress.Hex(),
	}); err != nil {
		return err
	}

	// Atomically enqueue all dispatch jobs within the same transaction
	if _, err := w.wc.queueClient.InsertManyTx(ctx, dbTx, dispatchParams); err != nil {
		return err
	}

	return dbTx.Commit(ctx)
}
