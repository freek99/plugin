package executor

import (
	"gitlab.33.cn/chain33/chain33/types"
	rTy "gitlab.33.cn/chain33/plugin/plugin/dapp/relay/types"
)

func (r *relay) Query_GetRelayOrderByStatus(in *rTy.ReqRelayAddrCoins) (types.Message, error) {
	if in == nil {
		return nil, types.ErrInvalidParam
	}
	return r.GetSellOrderByStatus(in)
}

func (r *relay) Query_GetSellRelayOrder(in *rTy.ReqRelayAddrCoins) (types.Message, error) {
	if in == nil {
		return nil, types.ErrInvalidParam
	}
	return r.GetSellRelayOrder(in)
}

func (r *relay) Query_GetBuyRelayOrder(in *rTy.ReqRelayAddrCoins) (types.Message, error) {
	if in == nil {
		return nil, types.ErrInvalidParam
	}
	return r.GetBuyRelayOrder(in)
}

func (r *relay) Query_GetBTCHeaderList(in *rTy.ReqRelayBtcHeaderHeightList) (types.Message, error) {
	if in == nil {
		return nil, types.ErrInvalidParam
	}
	db := newBtcStore(r.GetLocalDB())
	return db.getHeadHeightList(in)
}

func (r *relay) Query_GetBTCHeaderCurHeight(in *rTy.ReqRelayQryBTCHeadHeight) (types.Message, error) {
	if in == nil {
		return nil, types.ErrInvalidParam
	}
	db := newBtcStore(r.GetLocalDB())
	return db.getBtcCurHeight(in)
}