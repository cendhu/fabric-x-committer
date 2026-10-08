/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dependencygraph

// forest is a weakly connected set of transactions linked by dependency edges.
// A waiting forest can still grow and merge with other waiting forests.
// An in-flight forest has been emitted and its membership is frozen until
// every member is validated.
// Dependency edges (dependsOnTxs) never cross forests. A dependency on an
// in-flight forest is tracked only at the forest level via blockedBy and blocks.
type forest struct {
	// txs holds the members. It is unordered while waiting and topologically
	// ordered once emitted.
	txs TxNodeBatch
	// inFlight is true once the forest has been emitted.
	inFlight bool
	// pending is the number of members whose validation status has not been
	// received. It is set on emit.
	pending int
	// blockedBy holds the in-flight forests this waiting forest waits for.
	// The forest is emitted once blockedBy becomes empty.
	blockedBy map[*forest]struct{}
	// blocks holds the waiting forests that wait for this in-flight forest.
	blocks map[*forest]struct{}
}

func newForest(tx *TransactionNode) *forest {
	f := &forest{
		txs:       TxNodeBatch{tx},
		blockedBy: make(map[*forest]struct{}),
		blocks:    make(map[*forest]struct{}),
	}
	tx.forest = f
	return f
}

// absorb moves all members and blockers of the waiting forest other into f.
// After this call, other is no longer referenced by any transaction or blocker.
func (f *forest) absorb(other *forest) {
	for _, tx := range other.txs {
		tx.forest = f
	}
	f.txs = append(f.txs, other.txs...)
	for b := range other.blockedBy {
		delete(b.blocks, other)
		f.blockOn(b)
	}
}

// blockOn records that the waiting forest f waits for the in-flight forest b.
func (f *forest) blockOn(b *forest) {
	f.blockedBy[b] = struct{}{}
	b.blocks[f] = struct{}{}
}

// topoSort returns the members ordered such that every prerequisite precedes
// its dependents (Kahn's algorithm). As dependency edges never cross forests,
// every edge in dependsOnTxs is internal. Edges always point from a newer
// transaction to an older one, hence the graph is acyclic by construction.
func (f *forest) topoSort() TxNodeBatch {
	inDegree := make(map[*TransactionNode]int, len(f.txs))
	children := make(map[*TransactionNode]TxNodeBatch, len(f.txs))
	queue := make(TxNodeBatch, 0, len(f.txs))
	for _, tx := range f.txs {
		inDegree[tx] = len(tx.dependsOnTxs)
		for _, dep := range tx.dependsOnTxs {
			children[dep] = append(children[dep], tx)
		}
		if len(tx.dependsOnTxs) == 0 {
			queue = append(queue, tx)
		}
	}

	sorted := make(TxNodeBatch, 0, len(f.txs))
	for len(queue) > 0 {
		tx := queue[0]
		queue = queue[1:]
		sorted = append(sorted, tx)
		for _, child := range children[tx] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	return sorted
}
