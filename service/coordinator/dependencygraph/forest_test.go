/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dependencygraph

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForestTopoSort(t *testing.T) {
	t.Parallel()
	keys := makeTestKeys(t, 4)

	t.Run("diamond", func(t *testing.T) {
		t.Parallel()
		a := createTxNode(t, nil, [][]byte{keys[0], keys[1]}, nil)
		b := createTxNode(t, nil, [][]byte{keys[0], keys[2]}, nil)
		c := createTxNode(t, nil, [][]byte{keys[1], keys[3]}, nil)
		d := createTxNode(t, nil, [][]byte{keys[2], keys[3]}, nil)
		b.addDependenciesAndUpdateDependents(TxNodeBatch{a})
		c.addDependenciesAndUpdateDependents(TxNodeBatch{a})
		d.addDependenciesAndUpdateDependents(TxNodeBatch{b, c})

		f := newTestForest(d, c, b, a) // members deliberately not in dependency order
		requireForest(t, f.topoSort(), a, b, c, d)
	})

	t.Run("multiple roots", func(t *testing.T) {
		t.Parallel()
		a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
		b := createTxNode(t, nil, [][]byte{keys[1]}, nil)
		c := createTxNode(t, nil, [][]byte{keys[0], keys[1]}, nil)
		c.addDependenciesAndUpdateDependents(TxNodeBatch{a, b})

		f := newTestForest(c, b, a)
		requireForest(t, f.topoSort(), a, b, c)
	})

	t.Run("singleton", func(t *testing.T) {
		t.Parallel()
		a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
		requireForest(t, newForest(a).topoSort(), a)
	})
}

func TestForestAbsorb(t *testing.T) {
	t.Parallel()
	keys := makeTestKeys(t, 3)
	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	c := createTxNode(t, nil, [][]byte{keys[2]}, nil)

	inFlight := newForest(c)
	inFlight.inFlight = true

	fa := newForest(a)
	fb := newForest(b)
	fb.blockOn(inFlight)

	fa.absorb(fb)

	require.ElementsMatch(t, TxNodeBatch{a, b}, fa.txs)
	require.Same(t, fa, b.forest)
	require.Contains(t, fa.blockedBy, inFlight)
	require.Contains(t, inFlight.blocks, fa)
	require.NotContains(t, inFlight.blocks, fb)
}

// newTestForest builds one waiting forest holding txs in the given order.
func newTestForest(txs ...*TransactionNode) *forest {
	f := newForest(txs[0])
	for _, tx := range txs[1:] {
		f.absorb(newForest(tx))
	}
	return f
}

// requireForest asserts that got holds exactly want, in an order where
// every prerequisite precedes its dependents.
func requireForest(t *testing.T, got TxNodeBatch, want ...*TransactionNode) {
	t.Helper()
	require.ElementsMatch(t, want, got)
	position := make(map[*TransactionNode]int, len(got))
	for i, tx := range got {
		position[tx] = i
	}
	for _, tx := range got {
		for _, dep := range tx.dependsOnTxs {
			require.Less(t, position[dep], position[tx], "prerequisite must precede dependent")
		}
	}
}
