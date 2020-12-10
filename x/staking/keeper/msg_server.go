package keeper

import (
	"context"

	tmstrings "github.com/tendermint/tendermint/libs/strings"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the bank MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

func (k msgServer) CreateValidator(goCtx context.Context, msg *types.MsgCreateValidator) (*types.MsgCreateValidatorResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	valAddr, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	if err != nil {
		return nil, err
	}

	// check to see if the pubkey or sender has been registered before
	if _, found := k.GetValidator(ctx, valAddr); found {
		return nil, types.ErrValidatorOwnerExists
	}

	pk, ok := msg.Pubkey.GetCachedValue().(cryptotypes.PubKey)
	if !ok {
		return nil, sdkerrors.Wrapf(sdkerrors.ErrInvalidType, "Expecting cryptotypes.PubKey, got %T", pk)
	}

	if _, found := k.GetValidatorByConsAddr(ctx, sdk.GetConsAddress(pk)); found {
		return nil, types.ErrValidatorPubKeyExists
	}

	bondDenom := k.BondDenom(ctx)
	if msg.Value.Denom != bondDenom {
		return nil, sdkerrors.Wrapf(types.ErrBadDenom, "got %s, expected %s", msg.Value.Denom, bondDenom)
	}

	if _, err := msg.Description.EnsureLength(); err != nil {
		return nil, err
	}

	cp := ctx.ConsensusParams()
	if cp != nil && cp.Validator != nil {
		if !tmstrings.StringInSlice(pk.Type(), cp.Validator.PubKeyTypes) {
			return nil, sdkerrors.Wrapf(
				types.ErrValidatorPubKeyTypeNotSupported,
				"got: %s, expected: %s", pk.Type(), cp.Validator.PubKeyTypes,
			)
		}
	}

	validator, err := types.NewValidator(valAddr, pk, msg.Description)
	if err != nil {
		return nil, err
	}
	commission := types.NewCommissionWithTime(
		msg.Commission.Rate, msg.Commission.MaxRate,
		msg.Commission.MaxChangeRate, ctx.BlockHeader().Time,
	)

	validator, err = validator.SetInitialCommission(commission)
	if err != nil {
		return nil, err
	}

	validator.MinSelfDelegation = msg.MinSelfDelegation

	k.SetValidator(ctx, validator)
	k.SetValidatorByConsAddr(ctx, validator)
	k.SetNewValidatorByPowerIndex(ctx, validator)

	// call the after-creation hook
	k.AfterValidatorCreated(ctx, validator.GetOperator())

	delegatorAddress, err := sdk.AccAddressFromBech32(msg.DelegatorAddress)
	if err != nil {
		return &types.MsgCreateValidatorResponse{}, err
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.BondDenom(ctx), msg.Value.Amount))
	if err := k.bankKeeper.DelegateCoinsFromAccountToModule(ctx, delegatorAddress, types.EpochTempPoolName, coins); err != nil {
		return &types.MsgCreateValidatorResponse{}, err
	}

	epochNumber := k.GetEpochNumber(ctx)
	k.SaveEpochAction(ctx, epochNumber, msg)

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeCreateValidator,
			sdk.NewAttribute(types.AttributeKeyValidator, msg.ValidatorAddress),
			sdk.NewAttribute(sdk.AttributeKeyAmount, msg.Value.Amount.String()),
		),
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(sdk.AttributeKeySender, msg.DelegatorAddress),
		),
	})

	return &types.MsgCreateValidatorResponse{}, nil
}

func (k msgServer) EditValidator(goCtx context.Context, msg *types.MsgEditValidator) (*types.MsgEditValidatorResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	// Queue epoch action and move all the execution logic to Epoch execution
	epochNumber := k.GetEpochNumber(ctx)
	k.SaveEpochAction(ctx, epochNumber, msg)

	// TODO should do validation by running with cachedCtx like gov proposal creation
	return &types.MsgEditValidatorResponse{}, nil
}

func (k msgServer) Delegate(goCtx context.Context, msg *types.MsgDelegate) (*types.MsgDelegateResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	delegatorAddress, err := sdk.AccAddressFromBech32(msg.DelegatorAddress)
	if err != nil {
		return &types.MsgDelegateResponse{}, err
	}

	bondDenom := k.BondDenom(ctx)
	if msg.Amount.Denom != bondDenom {
		return &types.MsgDelegateResponse{}, sdkerrors.Wrapf(types.ErrBadDenom, "got %s, expected %s", msg.Amount.Denom, bondDenom)
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.BondDenom(ctx), msg.Amount.Amount))
	if err := k.bankKeeper.DelegateCoinsFromAccountToModule(ctx, delegatorAddress, types.EpochTempPoolName, coins); err != nil {
		return &types.MsgDelegateResponse{}, err
	}

	// Queue epoch action and move all the execution logic to Epoch execution
	epochNumber := k.GetEpochNumber(ctx)
	k.SaveEpochAction(ctx, epochNumber, msg)

	// TODO should do validation by running with cachedCtx like gov proposal creation
	// To consider: cachedCtx could have status which contains all the other epoch actions
	// could add CancelDelegate since they can't do any action until Delegation finish
	return &types.MsgDelegateResponse{}, nil
}

func (k msgServer) BeginRedelegate(goCtx context.Context, msg *types.MsgBeginRedelegate) (*types.MsgBeginRedelegateResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	// Queue epoch action and move all the execution logic to Epoch execution
	epochNumber := k.GetEpochNumber(ctx)
	k.SaveEpochAction(ctx, epochNumber, msg)

	cacheCtx, _ := ctx.CacheContext()
	cacheCtx = cacheCtx.WithBlockHeight(k.GetNextEpochHeight(ctx))
	cacheCtx = cacheCtx.WithBlockTime(k.GetNextEpochTime(ctx))
	completionTime, err := k.EpochBeginRedelegate(cacheCtx, msg)
	if err != nil {
		return nil, err
	}
	return &types.MsgBeginRedelegateResponse{
		CompletionTime: completionTime,
	}, nil
}

func (k msgServer) Undelegate(goCtx context.Context, msg *types.MsgUndelegate) (*types.MsgUndelegateResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	// Queue epoch action and move all the execution logic to Epoch execution
	k.SaveEpochAction(ctx, 0, msg)

	cacheCtx, _ := ctx.CacheContext()
	cacheCtx = cacheCtx.WithBlockHeight(k.GetNextEpochHeight(ctx))
	cacheCtx = cacheCtx.WithBlockTime(k.GetNextEpochTime(ctx))
	completionTime, err := k.EpochUndelegate(cacheCtx, msg)
	if err != nil {
		return nil, err
	}
	return &types.MsgUndelegateResponse{
		CompletionTime: completionTime,
	}, nil
}