// Copyright Fuzamei Corp. 2018 All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package para

import (
	"errors"
	"time"

	"encoding/hex"

	"bytes"

	"sync/atomic"

	"github.com/33cn/chain33/common"
	"github.com/33cn/chain33/types"
	paraexec "github.com/33cn/plugin/plugin/dapp/paracross/executor"
	pt "github.com/33cn/plugin/plugin/dapp/paracross/types"
)

func (client *client) setLocalDb(set *types.LocalDBSet) error {
	//如果追赶上主链了，则落盘
	if atomic.LoadInt32(&client.isCaughtUp) == 1 {
		set.Txid = 1
	}

	msg := client.GetQueueClient().NewMessage("blockchain", types.EventSetValueByKey, set)
	err := client.GetQueueClient().Send(msg, true)
	if err != nil {
		return err
	}
	resp, err := client.GetQueueClient().Wait(msg)
	if err != nil {
		return err
	}
	if resp.GetData().(*types.Reply).IsOk {
		return nil
	}
	return errors.New(string(resp.GetData().(*types.Reply).GetMsg()))
}

func (client *client) getLocalDb(set *types.LocalDBGet, count int) ([][]byte, error) {
	msg := client.GetQueueClient().NewMessage("blockchain", types.EventGetValueByKey, set)
	err := client.GetQueueClient().Send(msg, true)
	if err != nil {
		return nil, err
	}
	resp, err := client.GetQueueClient().Wait(msg)
	if err != nil {
		return nil, err
	}

	reply := resp.GetData().(*types.LocalReplyValue)
	if len(reply.Values) != count {
		plog.Error("Parachain getLocalDb count not match", "expert", count, "real", len(reply.Values))
		return nil, types.ErrInvalidParam
	}

	return reply.Values, nil
}

func (client *client) addLocalBlock(height int64, block *pt.ParaLocalDbBlock) error {
	set := &types.LocalDBSet{}

	key := calcTitleHeightKey(types.GetTitle(), height)
	kv := &types.KeyValue{Key: key, Value: types.Encode(block)}
	set.KV = append(set.KV, kv)

	//两个key原子操作
	key = calcTitleLastHeightKey(types.GetTitle())
	kv = &types.KeyValue{Key: key, Value: types.Encode(&types.Int64{Data: height})}
	set.KV = append(set.KV, kv)

	return client.setLocalDb(set)
}

func (client *client) checkCommitTxSuccess(txs []*pt.TxDetail) {
	if atomic.LoadInt32(&client.isCaughtUp) != 1 || !client.commitMsgClient.isSendingCommitMsg() {
		return
	}

	txMap := make(map[string]bool)
	curTx := client.commitMsgClient.getCurrentTx()
	if types.IsParaExecName(string(curTx.Execer)) {
		for _, tx := range txs {
			if bytes.HasSuffix(tx.Tx.Execer, []byte(pt.ParaX)) && tx.Receipt.Ty == types.ExecOk {
				txMap[string(tx.Tx.Hash())] = true
			}
		}
	} else {
		//去主链查询
		receipt, _ := client.QueryTxOnMainByHash(curTx.Hash())
		if receipt != nil && receipt.Receipt.Ty == types.ExecOk {
			txMap[string(curTx.Hash())] = true
		}
	}

	client.commitMsgClient.checkSendingTxDone(txMap)

}

func (client *client) createLocalBlock(lastBlock *pt.ParaLocalDbBlock, txs []*types.Transaction, mainBlock *pt.ParaTxDetail) error {
	var newblock pt.ParaLocalDbBlock

	newblock.Height = lastBlock.Height + 1
	newblock.MainHash = mainBlock.Header.Hash
	newblock.MainHeight = mainBlock.Header.Height
	newblock.ParentMainHash = lastBlock.MainHash
	newblock.BlockTime = mainBlock.Header.BlockTime

	newblock.Txs = txs

	err := client.addLocalBlock(newblock.Height, &newblock)
	if err != nil {
		return err
	}
	client.checkCommitTxSuccess(mainBlock.TxDetails)
	return err
}

func (client *client) createLocalGenesisBlock(genesis *types.Block) error {
	return client.alignLocalBlock2ChainBlock(genesis)
}

func (client *client) delLocalBlock(height int64) error {
	set := &types.LocalDBSet{}
	key := calcTitleHeightKey(types.GetTitle(), height)
	kv := &types.KeyValue{Key: key, Value: nil}
	set.KV = append(set.KV, kv)

	//两个key原子操作
	key = calcTitleLastHeightKey(types.GetTitle())
	kv = &types.KeyValue{Key: key, Value: types.Encode(&types.Int64{Data: height - 1})}
	set.KV = append(set.KV, kv)

	return client.setLocalDb(set)
}

// localblock 设置到当前高度，当前高度后面block会被新的区块覆盖
func (client *client) removeLocalBlocks(curHeight int64) error {
	set := &types.LocalDBSet{}

	key := calcTitleLastHeightKey(types.GetTitle())
	kv := &types.KeyValue{Key: key, Value: types.Encode(&types.Int64{Data: curHeight})}
	set.KV = append(set.KV, kv)

	return client.setLocalDb(set)
}

func (client *client) getLastLocalHeight() (int64, error) {
	key := calcTitleLastHeightKey(types.GetTitle())
	set := &types.LocalDBGet{Keys: [][]byte{key}}
	value, err := client.getLocalDb(set, len(set.Keys))
	if err != nil {
		return -1, err
	}
	if value[0] == nil {
		return -1, types.ErrNotFound
	}

	height := &types.Int64{}
	err = types.Decode(value[0], height)
	if err != nil {
		return -1, err
	}
	return height.Data, nil

}

func (client *client) getLocalBlockByHeight(height int64) (*pt.ParaLocalDbBlock, error) {
	key := calcTitleHeightKey(types.GetTitle(), height)
	set := &types.LocalDBGet{Keys: [][]byte{key}}

	value, err := client.getLocalDb(set, len(set.Keys))
	if err != nil {
		return nil, err
	}
	if value[0] == nil {
		return nil, types.ErrNotFound
	}

	var block pt.ParaLocalDbBlock
	err = types.Decode(value[0], &block)
	if err != nil {
		return nil, err
	}
	return &block, nil

}

func (client *client) getLocalBlockSeq(height int64) (int64, []byte, error) {
	lastBlock, err := client.getLocalBlockByHeight(height)
	if err != nil {
		return -2, nil, err
	}

	//如果当前mainHash对应seq获取不到，返回0 seq，和当前hash，去switchLocalHashMatchedBlock里面回溯查找
	mainSeq, err := client.GetSeqByHashOnMainChain(lastBlock.MainHash)
	if err != nil {
		return 0, lastBlock.MainHash, nil
	}
	return mainSeq, lastBlock.MainHash, nil

}

//根据匹配上的chainblock，设置当前localdb block
func (client *client) alignLocalBlock2ChainBlock(chainBlock *types.Block) error {
	localBlock := &pt.ParaLocalDbBlock{
		Height:     chainBlock.Height,
		MainHeight: chainBlock.MainHeight,
		MainHash:   chainBlock.MainHash,
		BlockTime:  chainBlock.BlockTime,
	}

	return client.addLocalBlock(localBlock.Height, localBlock)

}

//如果localdb里面没有信息，就从chain block返回，至少有创世区块，然后进入循环匹配切换场景
func (client *client) getLastLocalBlockSeq() (int64, []byte, error) {
	height, err := client.getLastLocalHeight()
	if err == nil {
		mainSeq, mainHash, err := client.getLocalBlockSeq(height)
		if err == nil {
			return mainSeq, mainHash, nil
		}
	}

	plog.Info("Parachain getLastLocalBlockSeq from block")
	//说明localDb获取存在错误，从chain获取
	mainSeq, chainBlock, err := client.getLastBlockMainInfo()
	if err != nil {
		return -2, nil, err
	}

	//chain block中获取成功，设置last local block和找到的chainBlock main高度和mainhash对齐
	err = client.alignLocalBlock2ChainBlock(chainBlock)
	if err != nil {
		return -2, nil, err
	}
	return mainSeq, chainBlock.MainHash, nil

}

func (client *client) getLastLocalBlock() (*pt.ParaLocalDbBlock, error) {
	height, err := client.getLastLocalHeight()
	if err != nil {
		return nil, err
	}

	return client.getLocalBlockByHeight(height)
}

//genesis block scenario
func (client *client) syncFromGenesisBlock() (int64, *types.Block, error) {
	lastSeq, lastBlock, err := client.getLastBlockMainInfo()
	if err != nil {
		plog.Error("Parachain getLastBlockInfo fail", "err", err)
		return -2, nil, err
	}
	plog.Info("syncFromGenesisBlock sync from height 0")
	return lastSeq, lastBlock, nil
}

func (client *client) getMatchedBlockOnChain(startHeight int64) (int64, *types.Block, error) {
	lastBlock, err := client.RequestLastBlock()
	if err != nil {
		plog.Error("Parachain RequestLastBlock fail", "err", err)
		return -2, nil, err
	}

	if lastBlock.Height == 0 {
		return client.syncFromGenesisBlock()
	}

	if startHeight == 0 || startHeight > lastBlock.Height {
		startHeight = lastBlock.Height
	}

	depth := searchHashMatchDepth
	for height := startHeight; height > 0 && depth > 0; height-- {
		block, err := client.GetBlockByHeight(height)
		if err != nil {
			return -2, nil, err
		}
		//当前block结构已经有mainHash和MainHeight但是从blockchain获取的block还没有写入，以后如果获取到，可以替换从minerTx获取
		plog.Info("switchHashMatchedBlock", "lastParaBlockHeight", height, "mainHeight",
			block.MainHeight, "mainHash", hex.EncodeToString(block.MainHash))
		mainSeq, err := client.GetSeqByHashOnMainChain(block.MainHash)
		if err != nil {
			depth--
			if depth == 0 {
				plog.Error("switchHashMatchedBlock depth overflow", "last info:mainHeight", block.MainHeight,
					"mainHash", hex.EncodeToString(block.MainHash), "search startHeight", lastBlock.Height, "curHeight", height,
					"search depth", searchHashMatchDepth)
				panic("search HashMatchedBlock overflow, re-setting search depth and restart to try")
			}
			if height == 1 {
				plog.Error("switchHashMatchedBlock search to height=1 not found", "lastBlockHeight", lastBlock.Height,
					"height1 mainHash", hex.EncodeToString(block.MainHash))
				return client.syncFromGenesisBlock()

			}
			continue
		}

		plog.Info("getMatchedBlockOnChain succ", "currHeight", height, "initHeight", lastBlock.Height,
			"new currSeq", mainSeq, "new preMainBlockHash", hex.EncodeToString(block.MainHash))
		return mainSeq, block, nil
	}
	return -2, nil, pt.ErrParaCurHashNotMatch
}

func (client *client) switchMatchedBlockOnChain(startHeight int64) (int64, []byte, error) {
	mainSeq, chainBlock, err := client.getMatchedBlockOnChain(startHeight)
	if err != nil {
		return -2, nil, err
	}
	//chain block中获取成功，设置last local block和找到的chainBlock main高度和mainhash对齐
	err = client.alignLocalBlock2ChainBlock(chainBlock)
	if err != nil {
		return -2, nil, err
	}
	return mainSeq, chainBlock.MainHash, nil
}

func (client *client) switchHashMatchedBlock() (int64, []byte, error) {
	mainSeq, mainHash, err := client.switchLocalHashMatchedBlock()
	if err != nil {
		return client.switchMatchedBlockOnChain(0)
	}
	return mainSeq, mainHash, nil
}

//
func (client *client) switchLocalHashMatchedBlock() (int64, []byte, error) {
	lastBlock, err := client.getLastLocalBlock()
	if err != nil {
		plog.Error("Parachain RequestLastBlock fail", "err", err)
		return -2, nil, err
	}

	for height := lastBlock.Height; height >= 0; height-- {
		block, err := client.getLocalBlockByHeight(height)
		if err != nil {
			return -2, nil, err
		}
		//当前block结构已经有mainHash和MainHeight但是从blockchain获取的block还没有写入，以后如果获取到，可以替换从minerTx获取
		plog.Info("switchLocalHashMatchedBlock", "height", height, "mainHeight", block.MainHeight, "mainHash", hex.EncodeToString(block.MainHash))
		mainSeq, err := client.GetSeqByHashOnMainChain(block.MainHash)
		if err != nil {
			continue
		}

		//remove fail, the para chain may be remove part, set the preMainBlockHash to nil, to match nothing, force to search from last
		err = client.removeLocalBlocks(height)
		if err != nil {
			return -2, nil, err
		}

		plog.Info("switchLocalHashMatchedBlock succ", "currHeight", height, "initHeight", lastBlock.Height,
			"currSeq", mainSeq, "currMainBlockHash", hex.EncodeToString(block.MainHash))
		return mainSeq, block.MainHash, nil
	}
	return -2, nil, pt.ErrParaCurHashNotMatch
}

func (client *client) getBatchFetchSeqCount(currSeq int64) (int64, error) {
	lastSeq, err := client.GetLastSeqOnMainChain()
	if err != nil {
		return 0, err
	}

	if lastSeq > currSeq {
		if lastSeq-currSeq > emptyBlockInterval {
			atomic.StoreInt32(&client.isCaughtUp, 0)
		} else {
			atomic.StoreInt32(&client.isCaughtUp, 1)
		}
		if batchFetchSeqEnable && lastSeq-currSeq > batchFetchSeqNum {
			return batchFetchSeqNum, nil
		}
		return 0, nil
	}

	if lastSeq == currSeq {
		return 0, nil
	}

	// lastSeq = currSeq -1
	if lastSeq+1 == currSeq {
		plog.Debug("Waiting new sequence from main chain")
		return 0, pt.ErrParaWaitingNewSeq
	}

	// lastSeq < currSeq-1
	return 0, pt.ErrParaCurHashNotMatch

}

// preBlockHash to identify the same main node
func (client *client) RequestTxOld(currSeq int64, preMainBlockHash []byte) ([]*types.Transaction, *types.BlockSeq, error) {
	plog.Debug("Para consensus RequestTx")
	lastSeq, err := client.GetLastSeqOnMainChain()
	if err != nil {
		return nil, nil, err
	}
	plog.Info("RequestTx", "LastMainSeq", lastSeq, "CurrSeq", currSeq)
	if lastSeq >= currSeq {
		blockSeq, err := client.GetBlockOnMainBySeq(currSeq)
		if err != nil {
			return nil, nil, err
		}

		if (bytes.Equal(preMainBlockHash, blockSeq.Detail.Block.ParentHash) && blockSeq.Seq.Type == addAct) ||
			(bytes.Equal(preMainBlockHash, blockSeq.Seq.Hash) && blockSeq.Seq.Type == delAct) {

			txs := paraexec.FilterTxsForPara(types.GetTitle(), blockSeq.Detail)
			plog.Info("GetCurrentSeq", "Len of txs", len(txs), "seqTy", blockSeq.Seq.Type)

			if lastSeq-currSeq > emptyBlockInterval {
				atomic.StoreInt32(&client.isCaughtUp, 0)
			} else {
				atomic.StoreInt32(&client.isCaughtUp, 1)
			}

			return txs, blockSeq, nil
		}
		//not consistent case be processed at below
		plog.Error("RequestTx", "preMainHash", hex.EncodeToString(preMainBlockHash), "currSeq preMainHash", hex.EncodeToString(blockSeq.Detail.Block.ParentHash),
			"currSeq mainHash", hex.EncodeToString(blockSeq.Seq.Hash), "curr seq", currSeq, "ty", blockSeq.Seq.Type, "currSeq Mainheight", blockSeq.Detail.Block.Height)
		return nil, nil, pt.ErrParaCurHashNotMatch
	}
	//lastSeq < CurrSeq case:
	//lastSeq = currSeq-1, main node not update
	if lastSeq+1 == currSeq {
		plog.Debug("Waiting new sequence from main chain")
		return nil, nil, pt.ErrParaWaitingNewSeq
	}

	// 1. lastSeq < currSeq-1
	// 2. lastSeq >= currSeq and seq not consistent or fork case
	return nil, nil, pt.ErrParaCurHashNotMatch
}

func (client *client) RequestTxOldVer(currSeq int64, preMainBlockHash []byte) (*pt.ParaTxDetails, error) {
	blockSeq, err := client.GetBlockOnMainBySeq(currSeq)
	if err != nil {
		return nil, err
	}

	txDetail := paraexec.BlockDetail2ParaTxs(blockSeq.Seq.Type, blockSeq.Seq.Hash, blockSeq.Detail)

	err = verifyTxDetailsHash(preMainBlockHash, txDetail)
	if err != nil {
		plog.Error("RequestTxOldVer", "curr seq", currSeq, "preMainBlockHash", hex.EncodeToString(preMainBlockHash))
		return nil, err
	}
	return &pt.ParaTxDetails{Items: []*pt.ParaTxDetail{txDetail}}, nil
}

func verifyTxDetailsHash(preMainBlockHash []byte, mainBlock *pt.ParaTxDetail) error {
	if (bytes.Equal(preMainBlockHash, mainBlock.Header.ParentHash) && mainBlock.Type == addAct) ||
		(bytes.Equal(preMainBlockHash, mainBlock.Header.Hash) && mainBlock.Type == delAct) {
		return nil
	}
	plog.Error("verifyTxDetailsHash", "preMainBlockHash", hex.EncodeToString(preMainBlockHash),
		"mainParentHash", hex.EncodeToString(mainBlock.Header.ParentHash), "mainHash", hex.EncodeToString(mainBlock.Header.Hash),
		"type", mainBlock.Type, "height", mainBlock.Header.Height)
	return pt.ErrParaCurHashNotMatch
}

func verifyTxDetails(preMainBlockHash []byte, mainBlocks *pt.ParaTxDetails) error {
	pre := preMainBlockHash
	for _, block := range mainBlocks.Items {
		err := verifyTxDetailsHash(pre, block)
		if err != nil {
			return err
		}
		pre = block.Header.Hash
	}
	return nil
}

func (client *client) RequestTxBatch(currSeq int64, count int64, preMainBlockHash []byte) (*pt.ParaTxDetails, error) {
	//req := &pt.ReqParaTxByTitle{Start: currSeq, End: currSeq + count, Title: types.GetTitle()}
	//items, err := client.GetBlockOnMainBySeq(req)
	//if err != nil {
	//	return nil, nil, err
	//}

	details := &pt.ParaTxDetails{}
	err := verifyTxDetails(preMainBlockHash, details)
	if err != nil {
		plog.Error("RequestTxBatch", "curr seq", currSeq, "count", count, "preMainBlockHash", hex.EncodeToString(preMainBlockHash))
		return nil, err
	}
	return details, nil
}

func (client *client) RequestTx(currSeq int64, count int64, preMainBlockHash []byte) (*pt.ParaTxDetails, error) {
	if !batchFetchSeqEnable {
		return client.RequestTxOldVer(currSeq, preMainBlockHash)
	}

	return client.RequestTxBatch(currSeq, count, preMainBlockHash)

}

func (client *client) processHashNotMatchError(currSeq int64, lastSeqMainHash []byte, err error) (int64, []byte, error) {
	if err == pt.ErrParaCurHashNotMatch {
		preSeq, preSeqMainHash, err := client.switchHashMatchedBlock()
		if err == nil {
			return preSeq + 1, preSeqMainHash, nil
		}
	}
	return currSeq, lastSeqMainHash, err
}

func (client *client) procLocalBlock(mainBlock *pt.ParaTxDetail) error {
	lastSeqMainHeight := mainBlock.Header.Height

	lastBlock, err := client.getLastLocalBlock()
	if err != nil {
		plog.Error("Parachain getLastLocalBlock", "err", err)
		return err
	}

	txs := paraexec.FilterTxsForParaPlus(types.GetTitle(), mainBlock)

	plog.Info("Parachain process block", "lastBlockHeight", lastBlock.Height, "currSeqMainHeight", lastSeqMainHeight,
		"lastBlockMainHeight", lastBlock.MainHeight, "lastBlockMainHash", common.ToHex(lastBlock.MainHash), "seqTy", mainBlock.Type)

	if mainBlock.Type == delAct {
		if len(txs) == 0 {
			if lastSeqMainHeight > lastBlock.MainHeight {
				return nil
			}
			plog.Info("Delete empty block")
		}
		return client.delLocalBlock(lastBlock.Height)

	} else if mainBlock.Type == addAct {
		if len(txs) == 0 {
			if lastSeqMainHeight-lastBlock.MainHeight < emptyBlockInterval {
				return nil
			}
			plog.Info("Create empty block")
		}
		return client.createLocalBlock(lastBlock, txs, mainBlock)

	}
	return types.ErrInvalidParam

}

func (client *client) procLocalBlocks(mainBlocks *pt.ParaTxDetails) error {
	for _, main := range mainBlocks.Items {
		err := client.procLocalBlock(main)
		if nil != err {
			return err
		}
	}
	return nil
}

func (client *client) CreateBlock() {
	lastSeq, lastSeqMainHash, err := client.getLastLocalBlockSeq()
	if err != nil {
		plog.Error("Parachain CreateBlock getLastLocalBlockSeq fail", "err", err.Error())
		return
	}
	currSeq := lastSeq + 1
	for {
		count, err := client.getBatchFetchSeqCount(currSeq)
		if err != nil {
			currSeq, lastSeqMainHash, err = client.processHashNotMatchError(currSeq, lastSeqMainHash, err)
			if err == nil {
				continue
			}
			time.Sleep(time.Second * time.Duration(blockSec))
			continue
		}

		plog.Info("Parachain CreateBlock", "curSeq", currSeq, "count", count, "lastSeqMainHash", common.ToHex(lastSeqMainHash))
		paraTxs, err := client.RequestTx(currSeq, count, lastSeqMainHash)
		if err != nil {
			currSeq, lastSeqMainHash, err = client.processHashNotMatchError(currSeq, lastSeqMainHash, err)
			continue
		}

		if count+1 != int64(len(paraTxs.Items)) {
			plog.Error("para CreateBlock count not match", "count", count+1, "items", len(paraTxs.Items))
			continue
		}

		err = client.procLocalBlocks(paraTxs)
		if err != nil {
			//根据localblock，重新搜索匹配
			lastSeqMainHash = nil
			plog.Error("para CreateBlock.procLocalBlocks", "err", err.Error())
			continue
		}

		//重新设定seq和lastSeqMainHash
		lastSeqMainHash = paraTxs.Items[count].Header.Hash
		if paraTxs.Items[count].Type == delAct {
			lastSeqMainHash = paraTxs.Items[count].Header.ParentHash
		}
		currSeq = currSeq + count + 1

	}
}
