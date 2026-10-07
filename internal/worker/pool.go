package worker

import (
	"context"
	"fmt"
	"math"
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
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

type (
	DeployPoolArgs struct {
		TrackingID                 string   `json:"trackingId"`
		PoolName                   string   `json:"poolName"`
		PoolSymbol                 string   `json:"poolSymbol"`
		PoolDecimals               string   `json:"poolDecimals"`
		Owner                      string   `json:"owner"`
		QuoterType                 string   `json:"quoterType"`
		FeesDecoupled              bool     `json:"feesDecoupled"`
		FeeAddress                 string   `json:"feeAddress"`
		FeePolicyDefaultFee        string   `json:"feePolicyDefaultFee"`
		BaseCurrency               string   `json:"baseCurrency"`
		UnitOfAccount              string   `json:"unitOfAccount"`
		FactoryAddress             string   `json:"factoryAddress"`
		SwapPoolImplAddress        string   `json:"swapPoolImplAddress"`
		TokenIndexImplAddress      string   `json:"tokenIndexImplAddress"`
		LimiterImplAddress         string   `json:"limiterImplAddress"`
		FeePolicyImplAddress       string   `json:"feePolicyImplAddress"`
		QuoterImplAddress          string   `json:"quoterImplAddress"`
		DecimalQuoterAddress       string   `json:"decimalQuoterAddress"`
		ProtocolFeeController      string   `json:"protocolFeeController"`
		SwapPoolPlatformContractID uint64   `json:"swapPoolPlatformContractId"`
		Description                string   `json:"description"`
		Website                    string   `json:"website"`
		Email                      string   `json:"email"`
		Phone                      string   `json:"phone"`
		Terms                      string   `json:"terms"`
		LocationName               string   `json:"locationName"`
		GeoLat                     *float64 `json:"geoLat"`
		GeoLng                     *float64 `json:"geoLng"`
		ImageURL                   string   `json:"imageUrl"`
		BannerURL                  string   `json:"bannerUrl"`
		Tags                       []string `json:"tags"`
	}

	DeployPoolWorker struct {
		river.WorkerDefaults[DeployPoolArgs]

		wc *WorkerContainer
	}
)

var (
	funcProtocolFeeRecipient = w3.MustNewFunc("getProtocolFeeRecipient()", "address")
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

	// The protocol fee controller is the one pool dependency that is not
	// deployed per pool, so a bad address in protocol_addresses is baked into
	// every pool initialized against it and SwapPool has no setter to correct
	// it. The implementation address passes an eth_getCode check — its
	// constructor disables initializers, so it answers every getter with a
	// zero — which is how it came to be seeded there in the first place.
	if err := w.wc.mustInitializedFeeController(ctx, args.ProtocolFeeController); err != nil {
		return err
	}

	factoryAddress := common.HexToAddress(args.FactoryAddress)

	funcPredict := w3.MustNewFunc("predictDeterministicAddress(bytes32)", "address")
	predictAddress := func(salt common.Hash) (common.Address, error) {
		var addr common.Address
		err := w.wc.chainProvider.Client.CallCtx(ctx, eth.CallFunc(factoryAddress, funcPredict, salt).Returns(&addr))
		return addr, err
	}

	// decimals is a stateless smart contract so we use the single deployed instance
	needsQuoterProxy := args.QuoterType != "decimal"
	// TokenIndex, Limiter, FeePolicy
	depCount := 3
	if needsQuoterProxy {
		depCount++
	}
	// +1 for SwapPool
	totalDeployments := depCount + 1

	tokenIndexSalt := createSalt(signerAddress, args.TrackingID, "TokenIndex")
	limiterSalt := createSalt(signerAddress, args.TrackingID, "Limiter")
	feePolicySalt := createSalt(signerAddress, args.TrackingID, "FeePolicy")
	swapPoolSalt := createSalt(signerAddress, args.TrackingID, "SwapPool")

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
		quoterSalt = createSalt(signerAddress, args.TrackingID, "Quoter")
		quoterAddr, err = predictAddress(quoterSalt)
		if err != nil {
			return err
		}
	} else {
		quoterAddr = common.HexToAddress(args.DecimalQuoterAddress)
	}

	dbTx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer dbTx.Rollback(ctx)

	// Acquire consecutive nonces for all deployments.
	signerNonces := make([]uint64, totalDeployments)
	for i := range totalDeployments {
		nonce, err := w.wc.store.AcquireNonce(ctx, dbTx, signerAddress.Hex())
		if err != nil {
			return err
		}
		signerNonces[i] = nonce
	}

	ownerAddress := common.HexToAddress(args.Owner)

	// 1. TokenUniqueSymbolIndex
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

	tokenIndexPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_TOKEN_INDEX_DEPLOY, signerAddress.Hex(), signerNonces[0], tokenIndexSignedTx)
	if err != nil {
		return err
	}

	// 2. Limiter
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

	limiterPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_LIMITER_DEPLOY, signerAddress.Hex(), signerNonces[1], limiterSignedTx)
	if err != nil {
		return err
	}

	// 3. FeePolicy
	feePolicyPercent, err := strconv.ParseFloat(args.FeePolicyDefaultFee, 64)
	if err != nil {
		return err
	}
	feePolicyDefaultFee := feePercentToPPM(feePolicyPercent)
	feePolicyPPM := float64(feePolicyDefaultFee)

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

	feePolicyPrepared, err := w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, store.POOL_FEE_POLICY_DEPLOY, signerAddress.Hex(), signerNonces[2], feePolicySignedTx)
	if err != nil {
		return err
	}

	// 4. Quoter proxy (if oracle/relative)
	var quoterPrepared *preparedTx
	if needsQuoterProxy {
		var quoterInit []byte
		var quoterOTXType string

		switch args.QuoterType {
		case "oracle":
			quoterInit, err = oraclequoter.EncodeInit(oraclequoter.InitArgs{
				Owner:        ownerAddress,
				BaseCurrency: common.HexToAddress(args.BaseCurrency),
			})
			quoterOTXType = store.POOL_ORACLE_QUOTER_DEPLOY
		case "relative":
			quoterInit, err = relativequoter.EncodeInit(relativequoter.InitArgs{
				Owner: ownerAddress,
			})
			quoterOTXType = store.POOL_RELATIVE_QUOTER_DEPLOY
		default:
			// Reachable only for a job queued before quoterType was required.
			// Falling through left the proxy pointing at the zero address with
			// empty initialization data.
			return fmt.Errorf("unsupported quoter type %q", args.QuoterType)
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

		quoterPrepared, err = w.wc.insertTrackedTx(ctx, dbTx, args.TrackingID, quoterOTXType, signerAddress.Hex(), signerNonces[3], quoterSignedTx)
		if err != nil {
			return err
		}
	}

	// 5. SwapPool
	// Decoupled fees go to the account the request named; jobs queued before
	// the field existed carry none and keep paying the owner, as they always did.
	feeAddress := ownerAddress
	if args.FeeAddress != "" {
		feeAddress = common.HexToAddress(args.FeeAddress)
	}
	feeAddressHex := feeAddress.Hex()
	swapPoolInit, err := swappool.EncodeInit(swappool.InitArgs{
		Name:                  args.PoolName,
		Symbol:                args.PoolSymbol,
		Decimals:              uint8(poolDecimals),
		Owner:                 ownerAddress,
		FeePolicy:             feePolicyAddr,
		FeeAddress:            feeAddress,
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

	deployedPoolID, err := w.wc.store.InsertDeployedPool(ctx, dbTx, store.DeployedPool{
		OTXID:                        swapPoolPrepared.OTXID,
		PlatformContractID:           args.SwapPoolPlatformContractID,
		Name:                         args.PoolName,
		Symbol:                       args.PoolSymbol,
		Decimals:                     uint8(poolDecimals),
		TokenRegistryAddress:         tokenIndexAddr.Hex(),
		LimiterAddress:               limiterAddr.Hex(),
		FeePolicyAddress:             feePolicyAddr.Hex(),
		QuoterAddress:                quoterAddr.Hex(),
		QuoterType:                   nilIfEmpty(args.QuoterType),
		ProtocolFeeControllerAddress: args.ProtocolFeeController,
		FeesDecoupled:                args.FeesDecoupled,
		// Recorded so the catalog promotion has a fee and a recipient to copy.
		// feeAddress is the resolved one, not args.FeeAddress: a request that
		// named none still pays the owner, and the catalog should say so rather
		// than report the pool as having no recipient.
		FeePPM:        &feePolicyPPM,
		FeeAddress:    &feeAddressHex,
		Admin:         ownerAddress.Hex(),
		Description:   nilIfEmpty(args.Description),
		UnitOfAccount: nilIfEmpty(args.UnitOfAccount),
		Website:       nilIfEmpty(args.Website),
		Email:         nilIfEmpty(args.Email),
		Phone:         nilIfEmpty(args.Phone),
		Terms:         nilIfEmpty(args.Terms),
		LocationName:  nilIfEmpty(args.LocationName),
		GeoLat:        args.GeoLat,
		GeoLng:        args.GeoLng,
		ImageURL:      nilIfEmpty(args.ImageURL),
		BannerURL:     nilIfEmpty(args.BannerURL),
	})
	if err != nil {
		return err
	}

	for _, tag := range args.Tags {
		if err := w.wc.store.InsertDeployedPoolTag(ctx, dbTx, deployedPoolID, tag); err != nil {
			return err
		}
	}

	dispatchParams := []river.InsertManyParams{
		{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      tokenIndexPrepared.OTXID,
				RawTx:      tokenIndexPrepared.RawHex,
				TxHash:     tokenIndexPrepared.Hash,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		},
		{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      limiterPrepared.OTXID,
				RawTx:      limiterPrepared.RawHex,
				TxHash:     limiterPrepared.Hash,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		},
		{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      feePolicyPrepared.OTXID,
				RawTx:      feePolicyPrepared.RawHex,
				TxHash:     feePolicyPrepared.Hash,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		},
	}
	if quoterPrepared != nil {
		dispatchParams = append(dispatchParams, river.InsertManyParams{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      quoterPrepared.OTXID,
				RawTx:      quoterPrepared.RawHex,
				TxHash:     quoterPrepared.Hash,
			},
			InsertOpts: &river.InsertOpts{Priority: 2},
		})
	}
	dispatchParams = append(dispatchParams, river.InsertManyParams{
		Args: DispatchArgs{
			TrackingID: args.TrackingID,
			OTXID:      swapPoolPrepared.OTXID,
			RawTx:      swapPoolPrepared.RawHex,
			TxHash:     swapPoolPrepared.Hash,
		},
		InsertOpts: &river.InsertOpts{Priority: 1},
	})

	// Atomically enqueue all dispatch jobs within the same transaction
	if _, err := w.wc.queueClient.InsertManyTx(ctx, dbTx, dispatchParams); err != nil {
		return err
	}

	return dbTx.Commit(ctx)
}

// mustInitializedFeeController rejects a protocol fee controller that is not a
// live initialized instance. The zero address is allowed and means the pool
// charges no protocol fee, which is what SwapPool reads it as.
func (w *WorkerContainer) mustInitializedFeeController(ctx context.Context, address string) error {
	controller := common.HexToAddress(address)
	if controller == (common.Address{}) {
		return nil
	}

	var recipient common.Address
	if err := w.chainProvider.Client.CallCtx(
		ctx,
		eth.CallFunc(controller, funcProtocolFeeRecipient).Returns(&recipient),
	); err != nil {
		return fmt.Errorf("protocol fee controller %s is not callable: %w", controller, err)
	}
	if recipient == (common.Address{}) {
		return fmt.Errorf("protocol fee controller %s has no recipient; it is an uninitialized implementation, not a deployed controller", controller)
	}

	return nil
}

// feePercentToPPM converts a fee percentage to parts per million. It rounds
// rather than truncates: float64 holds 0.57 just below the decimal value, so
// uint64(0.57 * 10_000) is 5699, and 612 of the 10_001 two-decimal percentages
// the API accepts would be stored a PPM light.
func feePercentToPPM(percent float64) uint64 {
	return uint64(math.Round(percent * 10_000))
}
