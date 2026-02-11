/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sidecar

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	"github.com/hyperledger/fabric-x-committer/api/types"
	"github.com/hyperledger/fabric-x-committer/loadgen/workload"
	"github.com/hyperledger/fabric-x-committer/utils"
	"github.com/hyperledger/fabric-x-committer/utils/logging"
)

func BenchmarkMapBlock(b *testing.B) {
	logging.SetupWithConfig(&logging.Config{Enabled: false})
	txs := workload.GenerateTransactions(b, workload.DefaultProfile(8), b.N)
	block := workload.MapToOrdererBlock(1, txs)

	workers := runtime.GOMAXPROCS(0)

	var txIDToHeight utils.SyncMap[string, types.Height]
	b.ResetTimer()
	mappedBlock, err := mapBlock(block, &txIDToHeight, workers)
	b.StopTimer()
	require.NoError(b, err, "This can never occur unless there is a bug in the relay.")
	require.NotNil(b, mappedBlock)
}

func BenchmarkMapBlockBySize(b *testing.B) {
	logging.SetupWithConfig(&logging.Config{Enabled: false})

	workers := runtime.GOMAXPROCS(0)

	for _, blockSize := range []int{100, 500, 1000, 5000} {
		b.Run(fmt.Sprintf("txs=%d", blockSize), func(b *testing.B) {
			txs := workload.GenerateTransactions(b, workload.DefaultProfile(8), blockSize)
			block := workload.MapToOrdererBlock(1, txs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var txIDToHeight utils.SyncMap[string, types.Height]
				_, err := mapBlock(block, &txIDToHeight, workers)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestBlockMapping(t *testing.T) {
	t.Parallel()
	txb := &workload.TxBuilder{ChannelID: "chan"}
	txs, expected := MalformedTxTestCases(txb)
	expectedBlockSize := 0
	expectedRejected := 0
	for i, e := range expected {
		if !IsStatusStoredInDB(e) {
			continue
		}
		expected[i] = statusNotYetValidated
		expectedBlockSize++
		if e != protoblocktx.Status_COMMITTED {
			expectedRejected++
		}
	}
	lgTX := txb.MakeTx(txs[0].Tx)
	txs = append(txs, lgTX)
	expected = append(expected, protoblocktx.Status_REJECTED_DUPLICATE_TX_ID)

	var txIDToHeight utils.SyncMap[string, types.Height]
	txIDToHeight.Store(lgTX.Id, types.Height{})

	block := workload.MapToOrdererBlock(1, txs)
	mappedBlock, err := mapBlock(block, &txIDToHeight, 1)
	require.NoError(t, err, "This can never occur unless there is a bug in the relay.")

	require.NotNil(t, mappedBlock)
	require.NotNil(t, mappedBlock.block)
	require.NotNil(t, mappedBlock.withStatus)

	require.Equal(t, block, mappedBlock.withStatus.block)
	require.Equal(t, block.Header.Number, mappedBlock.blockNumber)
	require.Equal(t, expected, mappedBlock.withStatus.txStatus)

	require.Equal(t, expectedBlockSize+1, txIDToHeight.Count())
	require.Len(t, mappedBlock.block.Txs, expectedBlockSize-expectedRejected)
	require.Len(t, mappedBlock.block.Rejected, expectedRejected)
	require.Equal(t, expectedBlockSize, mappedBlock.withStatus.pendingCount)
}
