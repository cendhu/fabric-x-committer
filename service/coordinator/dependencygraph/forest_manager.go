/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dependencygraph

import (
	"context"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/hyperledger/fabric-x-committer/utils"
	"github.com/hyperledger/fabric-x-committer/utils/channel"
	"github.com/hyperledger/fabric-x-committer/utils/monitoring"
	"github.com/hyperledger/fabric-x-committer/utils/monitoring/promutil"
)

type (
	// ForestManager is an alternative to Manager. Instead of emitting individual
	// dependency-free transactions, it emits dependency-free forests. A forest is a
	// weakly connected set of transactions linked by dependency edges, ordered such
	// that every prerequisite precedes its dependents. A transaction depending on a
	// member of an emitted forest waits until every member of that forest is validated.
	ForestManager struct {
		localDepConstructor *localDependencyConstructor
		forestDepManager    *forestDependencyManager
		parameters          *ForestParameters
	}

	// ForestParameters holds the configuration for the forest dependency manager.
	ForestParameters struct {
		// IncomingTxs is the channel for the manager to receive incoming transactions.
		IncomingTxs <-chan *TransactionBatch
		// OutgoingDepFreeForests is the channel for the manager to send dependency-free
		// forests. The outer slice holds forests; each inner slice holds one forest's
		// transactions in a valid execution order. The consumer owns the emitted slices.
		OutgoingDepFreeForests chan<- []TxNodeBatch
		// IncomingValidatedTxsNode is the channel for the manager to receive validated
		// transactions. Every transaction of an emitted forest must be reported exactly
		// once, in any grouping.
		IncomingValidatedTxsNode <-chan TxNodeBatch
		// NumOfLocalDepConstructors defines the number of local dependency constructors.
		NumOfLocalDepConstructors int
		// WaitingTxsLimit defines the maximum number of transactions held by the manager.
		WaitingTxsLimit int
		// PrometheusMetricsProvider is the provider for Prometheus metrics.
		PrometheusMetricsProvider *monitoring.Provider
	}

	// forestDependencyManager groups transactions into forests and decides when each
	// forest is emitted and when it is complete.
	forestDependencyManager struct {
		incomingTxsNode  <-chan *transactionNodeBatch
		outgoingForests  chan<- []TxNodeBatch
		validatedTxsNode <-chan TxNodeBatch

		// mu protects detector, ready, and every forest and its members' forest fields.
		mu sync.Mutex
		// detector holds the reads and writes of every transaction that is not part of
		// a completed forest.
		detector *dependencyDetector
		// ready holds emitted forests not yet written to outgoingForests.
		ready []TxNodeBatch
		// readySignal wakes the output goroutine. Sends never block (capacity 1).
		readySignal chan struct{}

		waitingTxsSlots *utils.Slots
		metrics         *perfMetrics
	}

	forestDepConfig struct {
		incomingTxsNode  <-chan *transactionNodeBatch
		outgoingForests  chan<- []TxNodeBatch
		validatedTxsNode <-chan TxNodeBatch
		waitingTxsLimit  int
		metrics          *perfMetrics
	}
)

// NewForestManager creates a new forest dependency manager.
func NewForestManager(p *ForestParameters) *ForestManager {
	outgoingTxsNodeWithLocalDep := make(chan *transactionNodeBatch, cap(p.IncomingTxs))

	metrics := newPerformanceMetrics(p.PrometheusMetricsProvider, &managerQueues{
		ldgInput: p.IncomingTxs,
		gdgInput: outgoingTxsNodeWithLocalDep,
	})

	return &ForestManager{
		localDepConstructor: newLocalDependencyConstructor(p.IncomingTxs, outgoingTxsNodeWithLocalDep, metrics),
		forestDepManager: newForestDependencyManager(&forestDepConfig{
			incomingTxsNode:  outgoingTxsNodeWithLocalDep,
			outgoingForests:  p.OutgoingDepFreeForests,
			validatedTxsNode: p.IncomingValidatedTxsNode,
			waitingTxsLimit:  p.WaitingTxsLimit,
			metrics:          metrics,
		}),
		parameters: p,
	}
}

// Run starts the local dependency constructors and the forest dependency manager.
func (m *ForestManager) Run(ctx context.Context) {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		m.localDepConstructor.run(gCtx, m.parameters.NumOfLocalDepConstructors)
		return nil
	})

	g.Go(func() error {
		m.forestDepManager.run(gCtx)
		return nil
	})

	_ = g.Wait()
}

func newForestDependencyManager(c *forestDepConfig) *forestDependencyManager {
	logger.Info("Initializing newForestDependencyManager")
	return &forestDependencyManager{
		incomingTxsNode:  c.incomingTxsNode,
		outgoingForests:  c.outgoingForests,
		validatedTxsNode: c.validatedTxsNode,
		detector:         newDependencyDetector(),
		readySignal:      make(chan struct{}, 1),
		waitingTxsSlots:  utils.NewSlots(int64(c.waitingTxsLimit)),
		metrics:          c.metrics,
	}
}

func (dm *forestDependencyManager) run(ctx context.Context) {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		dm.constructDependencyGraph(gCtx)
		return nil
	})

	g.Go(func() error {
		dm.processValidatedTransactions(gCtx)
		return nil
	})

	g.Go(func() error {
		dm.outputFreedExistingTransactions(gCtx)
		return nil
	})

	_ = g.Wait()
}

func (dm *forestDependencyManager) constructDependencyGraph(ctx context.Context) {
	done := context.AfterFunc(ctx, dm.waitingTxsSlots.Broadcast)
	defer done()
	incomingTxsNode := channel.NewReader(ctx, dm.incomingTxsNode)
	for {
		batch, ok := incomingTxsNode.Read()
		if !ok {
			return
		}

		dm.waitingTxsSlots.Acquire(ctx, int64(len(batch.txsNode)))
		if ctx.Err() != nil {
			return
		}
		promutil.AddToGauge(dm.metrics.gdgWaitingTxCount, len(batch.txsNode))

		dm.mu.Lock()
		dm.addBatch(batch)
		dm.mu.Unlock()

		promutil.AddToCounter(dm.metrics.gdgTxProcessedTotal, len(batch.txsNode))
	}
}

// addBatch adds every transaction of the batch to the graph, and only then emits
// the forests of this batch that wait for no in-flight forest. Emitting only after
// the whole batch is added ensures that a later transaction in the batch cannot
// connect to an already emitted forest of the same batch.
// It must be called with dm.mu held.
func (dm *forestDependencyManager) addBatch(batch *transactionNodeBatch) {
	for _, tx := range batch.txsNode {
		dm.addTx(tx)
	}
	dm.detector.mergeWaitingTx(batch.localDepDetector)

	checked := make(map[*forest]struct{})
	for _, tx := range batch.txsNode {
		f := tx.forest
		if _, ok := checked[f]; ok {
			continue
		}
		checked[f] = struct{}{}
		if len(f.blockedBy) == 0 {
			dm.emit(f)
		}
	}
}

// addTx places tx in a waiting forest. Dependencies on waiting transactions become
// dependency edges and merge their forests with tx's forest. Dependencies on
// in-flight transactions only block tx's forest; no edge is stored.
// Local dependencies (within the batch) are already set by the local dependency
// constructor and always point to waiting transactions of this batch.
// It must be called with dm.mu held.
func (dm *forestDependencyManager) addTx(tx *TransactionNode) {
	waitingDeps, blockers := dm.splitGlobalDependencies(tx)
	if len(waitingDeps) > 0 && len(tx.dependsOnTxs) == 0 {
		// The local dependency constructor counts only transactions with local dependencies.
		promutil.AddToGauge(dm.metrics.dependentTxCount, 1)
	}
	tx.addDependenciesAndUpdateDependents(waitingDeps)

	// Join the largest connected forest and merge the others into it.
	var target *forest
	for _, dep := range tx.dependsOnTxs {
		if target == nil || len(dep.forest.txs) > len(target.txs) {
			target = dep.forest
		}
	}
	if target == nil {
		target = newForest(tx)
	} else {
		target.txs = append(target.txs, tx)
		tx.forest = target
	}
	for _, dep := range tx.dependsOnTxs {
		if dep.forest != target {
			target.absorb(dep.forest)
		}
	}
	for _, b := range blockers {
		target.blockOn(b)
	}
}

// splitGlobalDependencies returns the transactions from earlier batches that tx depends
// on and that are still waiting, and the in-flight forests that tx depends on.
// It must be called with dm.mu held.
func (dm *forestDependencyManager) splitGlobalDependencies(
	tx *TransactionNode,
) (waitingDeps TxNodeBatch, blockers []*forest) {
	for _, dep := range dm.detector.getDependenciesOf(tx) {
		if dep.forest.inFlight {
			blockers = append(blockers, dep.forest)
		} else {
			waitingDeps = append(waitingDeps, dep)
		}
	}
	return waitingDeps, blockers
}

// emit orders the forest, freezes it, and queues it for output.
// It must be called with dm.mu held.
func (dm *forestDependencyManager) emit(f *forest) {
	f.txs = f.topoSort()
	f.inFlight = true
	f.pending = len(f.txs)

	dependents := 0
	for _, tx := range f.txs {
		if len(tx.dependsOnTxs) > 0 {
			dependents++
		}
	}
	promutil.SubFromGauge(dm.metrics.dependentTxCount, dependents)

	// The consumer owns the emitted slice, so the manager keeps its own copy.
	dm.ready = append(dm.ready, slices.Clone(f.txs))
	select {
	case dm.readySignal <- struct{}{}:
	default:
	}
}

func (dm *forestDependencyManager) processValidatedTransactions(ctx context.Context) {
	validatedTxsNode := channel.NewReader(ctx, dm.validatedTxsNode)
	for {
		txsNode, ok := validatedTxsNode.Read()
		if !ok {
			return
		}

		dm.mu.Lock()
		released := dm.markValidated(txsNode)
		dm.mu.Unlock()

		dm.waitingTxsSlots.Release(int64(released))
		promutil.SubFromGauge(dm.metrics.gdgWaitingTxCount, released)
		promutil.AddToCounter(dm.metrics.gdgValidatedTxProcessedTotal, len(txsNode))
	}
}

// markValidated records the validation of each transaction. Once every member of a
// forest is validated, the forest is removed from the graph and every waiting forest
// that no longer waits for any in-flight forest is emitted. Transactions not in an
// in-flight forest (e.g., rejected transactions) and duplicates are ignored.
// It returns the number of transactions in completed forests.
// It must be called with dm.mu held.
func (dm *forestDependencyManager) markValidated(txsNode TxNodeBatch) int {
	released := 0
	for _, tx := range txsNode {
		f := tx.forest
		if f == nil || !f.inFlight || tx.validated {
			continue
		}
		tx.validated = true
		f.pending--
		if f.pending > 0 {
			continue
		}

		dm.detector.removeWaitingTx(f.txs)
		released += len(f.txs)
		for w := range f.blocks {
			delete(w.blockedBy, f)
			if len(w.blockedBy) == 0 {
				dm.emit(w)
			}
		}
		// Detach the completed forest so that it does not keep newer forests reachable.
		// Later duplicate feedback for its members is ignored as their forest is nil.
		f.blocks = nil
		for _, member := range f.txs {
			member.forest = nil
		}
	}
	return released
}

// outputFreedExistingTransactions writes the emitted forests to the output channel. It is the only
// writer of the output channel, so a slow consumer never blocks construction or
// validation processing.
func (dm *forestDependencyManager) outputFreedExistingTransactions(ctx context.Context) {
	outgoingForests := channel.NewWriter(ctx, dm.outgoingForests)
	for {
		select {
		case <-ctx.Done():
			return
		case <-dm.readySignal:
		}

		dm.mu.Lock()
		forests := dm.ready
		dm.ready = nil
		dm.mu.Unlock()

		if len(forests) > 0 && !outgoingForests.Write(forests) {
			return
		}
	}
}
