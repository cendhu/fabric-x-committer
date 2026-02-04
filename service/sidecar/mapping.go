/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sidecar

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"go.uber.org/zap/zapcore"

	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	"github.com/hyperledger/fabric-x-committer/api/protocoordinatorservice"
	"github.com/hyperledger/fabric-x-committer/api/types"
	"github.com/hyperledger/fabric-x-committer/service/verifier/policy"
	"github.com/hyperledger/fabric-x-committer/utils"
	"github.com/hyperledger/fabric-x-committer/utils/serialization"
)

type (
	blockMappingResult struct {
		blockNumber uint64
		block       *protocoordinatorservice.Batch
		withStatus  *blockWithStatus
		isConfig    bool
		// txIDToHeight is a reference to the relay map.
		txIDToHeight *utils.SyncMap[string, types.Height]
	}

	blockWithStatus struct {
		block        *common.Block
		txStatus     []protoblocktx.Status
		pendingCount int
	}
)

const (
	statusNotYetValidated = protoblocktx.Status_NOT_VALIDATED
	statusIdx             = int(common.BlockMetadataIndex_TRANSACTIONS_FILTER)
)

// parsedEnvelope holds the result of pre-parsing a single transaction envelope.
// The expensive operations (UnwrapEnvelopeLite, UnmarshalTx, verifyTxForm) are performed
// in parallel across goroutines, and the results are consumed sequentially by mapBlock.
type parsedEnvelope struct {
	hdr        *serialization.EnvelopeHeader
	tx         *protoblocktx.Tx
	hdrErr     error
	txErr      error
	formStatus protoblocktx.Status
}

// parseJob is a unit of work sent to pre-created worker goroutines.
// Each job processes a contiguous chunk of envelopes from a block.
type parseJob struct {
	data    [][]byte
	results []parsedEnvelope
	start   int
	end     int
	wg      *sync.WaitGroup
}

// txParser is a pool of pre-created worker goroutines that parse transaction
// envelopes in parallel. Workers are created once and reused across all blocks,
// avoiding repeated goroutine spawn/teardown overhead under sustained load.
type txParser struct {
	jobs    chan parseJob
	workers int
}

func newTxParser(workers int) *txParser {
	p := &txParser{
		jobs:    make(chan parseJob),
		workers: workers,
	}
	for range workers {
		go func() {
			for job := range p.jobs {
				for i := job.start; i < job.end; i++ {
					parseOneEnvelope(job.data[i], &job.results[i])
				}
				job.wg.Done()
			}
		}()
	}
	return p
}

func (p *txParser) close() {
	close(p.jobs)
}

// minTxsForParallelParse is the minimum number of transactions in a block
// before parallel parsing is used. Below this threshold, the synchronization
// overhead exceeds the parallelism benefit.
const minTxsForParallelParse = 200

// parseBlockEnvelopes pre-parses all transaction envelopes.
// When a txParser is provided and the block is large enough, it distributes the
// work across pre-created worker goroutines. UnwrapEnvelopeLite, UnmarshalTx,
// and verifyTxForm are all pure functions with no shared mutable state,
// so they can safely run concurrently.
func parseBlockEnvelopes(data [][]byte, parser *txParser) []parsedEnvelope {
	n := len(data)
	results := make([]parsedEnvelope, n)

	if parser == nil || n < minTxsForParallelParse {
		for i := range data {
			parseOneEnvelope(data[i], &results[i])
		}
		return results
	}

	workers := parser.workers
	if workers > n {
		workers = n
	}

	var wg sync.WaitGroup
	chunkSize := (n + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > n {
			end = n
		}
		wg.Add(1)
		parser.jobs <- parseJob{
			data:    data,
			results: results,
			start:   start,
			end:     end,
			wg:      &wg,
		}
	}
	wg.Wait()
	return results
}

func parseOneEnvelope(msg []byte, result *parsedEnvelope) {
	result.hdr, result.hdrErr = serialization.UnwrapEnvelopeLite(msg)
	if result.hdrErr != nil {
		return
	}
	if common.HeaderType(result.hdr.HeaderType) != common.HeaderType_MESSAGE {
		return
	}
	result.tx, result.txErr = serialization.UnmarshalTx(result.hdr.Data)
	if result.txErr != nil {
		return
	}
	result.formStatus = verifyTxForm(result.tx)
}

func mapBlock(block *common.Block, txIDToHeight *utils.SyncMap[string, types.Height], parser *txParser) (*blockMappingResult, error) {
	// Prepare block's metadata.
	if block.Metadata == nil {
		block.Metadata = &common.BlockMetadata{}
	}
	metadataSize := len(block.Metadata.Metadata)
	if metadataSize <= statusIdx {
		block.Metadata.Metadata = append(block.Metadata.Metadata, make([][]byte, statusIdx+1-metadataSize)...)
	}

	blockNumber := block.Header.Number

	if block.Data == nil {
		logger.Warnf("Received a block [%d] without data", block.Header.Number)
		return &blockMappingResult{
			blockNumber:  blockNumber,
			block:        &protocoordinatorservice.Batch{},
			withStatus:   &blockWithStatus{block: block},
			txIDToHeight: txIDToHeight,
		}, nil
	}

	txCount := len(block.Data.Data)
	mappedBlock := &blockMappingResult{
		blockNumber: blockNumber,
		block: &protocoordinatorservice.Batch{
			Txs:      make([]*protocoordinatorservice.Tx, 0, txCount),
			Rejected: make([]*protocoordinatorservice.TxStatusInfo, 0, txCount),
		},
		withStatus: &blockWithStatus{
			block:        block,
			txStatus:     make([]protoblocktx.Status, txCount),
			pendingCount: txCount,
		},
		txIDToHeight: txIDToHeight,
	}

	// Stage 1: Parse all envelopes in parallel (pure computation, no shared state).
	parsed := parseBlockEnvelopes(block.Data.Data, parser)

	// Stage 2: Sequential mapping using pre-parsed results (shared state updates).
	for msgIndex := range block.Data.Data {
		logger.Debugf("Mapping transaction [blk,tx] = [%d,%d]", blockNumber, msgIndex)
		err := mappedBlock.mapParsedMessage(uint32(msgIndex), block.Data.Data[msgIndex], &parsed[msgIndex]) //nolint:gosec // int -> uint32.
		if err != nil {
			// This can never occur unless there is a bug in the relay.
			return nil, err
		}
	}
	return mappedBlock, nil
}

func (b *blockMappingResult) mapParsedMessage(msgIndex uint32, msg []byte, p *parsedEnvelope) error {
	if p.hdrErr != nil {
		return b.rejectNonDBStatusTx(msgIndex, "", 0, protoblocktx.Status_MALFORMED_BAD_ENVELOPE, p.hdrErr.Error())
	}
	txID := p.hdr.TxId
	headerType := p.hdr.HeaderType
	if txID == "" || !utf8.ValidString(txID) {
		return b.rejectNonDBStatusTx(msgIndex, txID, headerType, protoblocktx.Status_MALFORMED_MISSING_TX_ID, "no TX ID")
	}

	switch common.HeaderType(headerType) {
	default:
		return b.rejectTx(msgIndex, txID, headerType, protoblocktx.Status_MALFORMED_UNSUPPORTED_ENVELOPE_PAYLOAD, "message type")
	case common.HeaderType_CONFIG:
		_, err := policy.ParsePolicyFromConfigTx(msg)
		if err != nil {
			return b.rejectTx(msgIndex, txID, headerType, protoblocktx.Status_MALFORMED_CONFIG_TX_INVALID, err.Error())
		}
		b.isConfig = true
		return b.appendTx(msgIndex, txID, headerType, configTx(msg))
	case common.HeaderType_MESSAGE:
		if p.txErr != nil {
			return b.rejectTx(msgIndex, txID, headerType, protoblocktx.Status_MALFORMED_BAD_ENVELOPE_PAYLOAD, p.txErr.Error())
		}
		if p.formStatus != statusNotYetValidated {
			return b.rejectTx(msgIndex, txID, headerType, p.formStatus, "malformed tx")
		}
		return b.appendTx(msgIndex, txID, headerType, p.tx)
	}
}

func (b *blockMappingResult) appendTx(txNum uint32, txID string, headerType int32, tx *protoblocktx.Tx) error {
	if idAlreadyExists, err := b.addTxIDMapping(txNum, txID, headerType); idAlreadyExists || err != nil {
		return err
	}
	b.block.Txs = append(b.block.Txs, &protocoordinatorservice.Tx{
		Ref:     types.TxRef(txID, b.blockNumber, txNum),
		Content: tx,
	})
	debugTx(txID, headerType, "included: %s", txID)
	return nil
}

func (b *blockMappingResult) rejectTx(
	txNum uint32, txID string, headerType int32, status protoblocktx.Status, reason string,
) error {
	if !IsStatusStoredInDB(status) {
		return b.rejectNonDBStatusTx(txNum, txID, headerType, status, reason)
	}
	if idAlreadyExists, err := b.addTxIDMapping(txNum, txID, headerType); idAlreadyExists || err != nil {
		return err
	}
	b.block.Rejected = append(b.block.Rejected, &protocoordinatorservice.TxStatusInfo{
		Ref:    types.TxRef(txID, b.blockNumber, txNum),
		Status: status,
	})
	debugTx(txID, headerType, "rejected: %s (%s)", &status, reason)
	return nil
}

// rejectNonDBStatusTx is used to reject with statuses that are not stored in the state DB.
// Namely, statuses for cases where we don't have a TX ID, or there is a TX ID duplication.
// For such cases, no notification will be given by the notification service.
func (b *blockMappingResult) rejectNonDBStatusTx(
	txNum uint32, txID string, headerType int32, status protoblocktx.Status, reason string,
) error {
	if IsStatusStoredInDB(status) {
		// This can never occur unless there is a bug in the relay.
		return errors.Newf("[BUG] status should be stored [blk:%d,num:%d]: %s", b.blockNumber, txNum, &status)
	}
	err := b.withStatus.setFinalStatus(txNum, status)
	if err != nil {
		return err
	}
	debugTx(txID, headerType, "excluded: %s (%s)", &status, reason)
	return nil
}

func (b *blockMappingResult) addTxIDMapping(txNum uint32, txID string, headerType int32) (idAlreadyExists bool, err error) {
	_, idAlreadyExists = b.txIDToHeight.LoadOrStore(txID, types.Height{
		BlockNum: b.blockNumber,
		TxNum:    txNum,
	})
	if idAlreadyExists {
		err = b.rejectNonDBStatusTx(txNum, txID, headerType, protoblocktx.Status_REJECTED_DUPLICATE_TX_ID, "duplicate tx")
	}
	return idAlreadyExists, err
}

func (b *blockWithStatus) setFinalStatus(txNum uint32, status protoblocktx.Status) error {
	if b.txStatus[txNum] != statusNotYetValidated {
		// This can never occur unless there is a bug in the relay or the coordinator.
		return errors.Newf("two results for a TX [blockNum: %d, txNum: %d]", b.block.Header.Number, txNum)
	}
	b.txStatus[txNum] = status
	b.pendingCount--
	return nil
}

func (b *blockWithStatus) setStatusMetadataInBlock() {
	statusMetadata := make([]byte, len(b.txStatus))
	for i, s := range b.txStatus {
		statusMetadata[i] = byte(s)
	}
	b.block.Metadata.Metadata[statusIdx] = statusMetadata
}

// IsStatusStoredInDB returns true if the given status code can be stored in the state DB.
func IsStatusStoredInDB(status protoblocktx.Status) bool {
	switch status {
	case protoblocktx.Status_MALFORMED_BAD_ENVELOPE,
		protoblocktx.Status_MALFORMED_MISSING_TX_ID,
		protoblocktx.Status_REJECTED_DUPLICATE_TX_ID:
		return false
	default:
		return true
	}
}

func debugTx(txID string, headerType int32, format string, a ...any) {
	if logger.Level() > zapcore.DebugLevel {
		return
	}
	hdr := "<no-header>"
	id := "<no-id>"
	if txID != "" {
		hdr = common.HeaderType(headerType).String()
		id = txID
	}
	logger.Debugf("TX type [%s] ID [%s]: %s", hdr, id, fmt.Sprintf(format, a...))
}

func configTx(value []byte) *protoblocktx.Tx {
	return &protoblocktx.Tx{
		Namespaces: []*protoblocktx.TxNamespace{{
			NsId:      types.ConfigNamespaceID,
			NsVersion: 0,
			BlindWrites: []*protoblocktx.Write{{
				Key:   []byte(types.ConfigKey),
				Value: value,
			}},
		}},
	}
}

// verifyTxForm verifies that a TX is not malformed.
// It returns status MALFORMED_<reason> if it is malformed, or not-validated otherwise.
func verifyTxForm(tx *protoblocktx.Tx) protoblocktx.Status {
	if len(tx.Namespaces) == 0 {
		return protoblocktx.Status_MALFORMED_EMPTY_NAMESPACES
	}
	if len(tx.Namespaces) != len(tx.Signatures) {
		return protoblocktx.Status_MALFORMED_MISSING_SIGNATURE
	}

	for i, ns := range tx.Namespaces {
		// Checks that the application does not submit a config TX.
		if ns.NsId == types.ConfigNamespaceID || policy.ValidateNamespaceID(ns.NsId) != nil {
			return protoblocktx.Status_MALFORMED_NAMESPACE_ID_INVALID
		}
		// Check for duplicate namespace IDs using O(n²) comparison.
		// Typical transactions have very few namespaces (1-3).
		for _, prev := range tx.Namespaces[:i] {
			if ns.NsId == prev.NsId {
				return protoblocktx.Status_MALFORMED_DUPLICATE_NAMESPACE
			}
		}

		if status := checkNamespaceFormation(ns); status != statusNotYetValidated {
			return status
		}
		if status := checkMetaNamespace(ns); status != statusNotYetValidated {
			return status
		}
	}
	return statusNotYetValidated
}

func checkNamespaceFormation(ns *protoblocktx.TxNamespace) protoblocktx.Status {
	if len(ns.ReadWrites) == 0 && len(ns.BlindWrites) == 0 {
		return protoblocktx.Status_MALFORMED_NO_WRITES
	}

	// Collect keys into a stack-allocated array to avoid heap allocation.
	// Typical namespaces have only a few keys, so 32 is more than enough.
	total := len(ns.ReadsOnly) + len(ns.ReadWrites) + len(ns.BlindWrites)
	var buf [32][]byte
	var keys [][]byte
	if total <= len(buf) {
		keys = buf[:0]
	} else {
		keys = make([][]byte, 0, total)
	}
	for _, r := range ns.ReadsOnly {
		keys = append(keys, r.Key)
	}
	for _, r := range ns.ReadWrites {
		keys = append(keys, r.Key)
	}
	for _, r := range ns.BlindWrites {
		keys = append(keys, r.Key)
	}
	return checkKeys(keys)
}

func checkMetaNamespace(txNs *protoblocktx.TxNamespace) protoblocktx.Status {
	if txNs.NsId != types.MetaNamespaceID {
		return statusNotYetValidated
	}
	if len(txNs.BlindWrites) > 0 {
		return protoblocktx.Status_MALFORMED_BLIND_WRITES_NOT_ALLOWED
	}

	nsUpdate := make(map[string]any)
	u := policy.GetUpdatesFromNamespace(txNs)
	if u == nil {
		return statusNotYetValidated
	}
	for _, pd := range u.NamespacePolicies.Policies {
		_, err := policy.ParseNamespacePolicyItem(pd)
		if err != nil {
			if errors.Is(err, policy.ErrInvalidNamespaceID) {
				return protoblocktx.Status_MALFORMED_NAMESPACE_ID_INVALID
			}
			return protoblocktx.Status_MALFORMED_NAMESPACE_POLICY_INVALID
		}
		if pd.Namespace == types.MetaNamespaceID {
			return protoblocktx.Status_MALFORMED_NAMESPACE_POLICY_INVALID
		}
		if _, ok := nsUpdate[pd.Namespace]; ok {
			return protoblocktx.Status_MALFORMED_NAMESPACE_POLICY_INVALID
		}
		nsUpdate[pd.Namespace] = nil
	}
	return statusNotYetValidated
}

// checkKeys verifies there are no duplicate keys and no nil keys.
// For small key sets (≤16), it uses O(n²) comparison to avoid map allocation.
func checkKeys(keys [][]byte) protoblocktx.Status {
	for i, k := range keys {
		if len(k) == 0 {
			return protoblocktx.Status_MALFORMED_EMPTY_KEY
		}
		for _, prev := range keys[:i] {
			if string(k) == string(prev) {
				return protoblocktx.Status_MALFORMED_DUPLICATE_KEY_IN_READ_WRITE_SET
			}
		}
	}
	return statusNotYetValidated
}
