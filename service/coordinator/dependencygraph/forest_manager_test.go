/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dependencygraph

import (
	"context"
	"testing"
	"time"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/stretchr/testify/require"

	"github.com/hyperledger/fabric-x-committer/api/servicepb"
	"github.com/hyperledger/fabric-x-committer/utils/monitoring"
	"github.com/hyperledger/fabric-x-committer/utils/test"
)

type forestTestEnv struct {
	incomingTxs  chan *transactionNodeBatch
	outgoing     chan []TxNodeBatch
	validatedTxs chan TxNodeBatch
	metrics      *perfMetrics
	dm           *forestDependencyManager
}

func newForestTestEnv(t *testing.T, waitingTxsLimit int) *forestTestEnv {
	t.Helper()
	incomingTxs := make(chan *transactionNodeBatch, 10)
	env := &forestTestEnv{
		incomingTxs:  incomingTxs,
		outgoing:     make(chan []TxNodeBatch, 10),
		validatedTxs: make(chan TxNodeBatch, 10),
		metrics: newPerformanceMetrics(monitoring.NewProvider(), &managerQueues{
			gdgInput: incomingTxs,
		}),
	}
	env.dm = newForestDependencyManager(&forestDepConfig{
		incomingTxsNode:  env.incomingTxs,
		outgoingForests:  env.outgoing,
		validatedTxsNode: env.validatedTxs,
		waitingTxsLimit:  waitingTxsLimit,
		metrics:          env.metrics,
	})
	test.RunServiceForTest(t.Context(), t, func(ctx context.Context) error {
		env.dm.run(ctx)
		return nil
	}, nil)
	return env
}

func TestForestIndependentTxs(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 10)
	keys := makeTestKeys(t, 3)

	t1 := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	t2 := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	t3 := createTxNode(t, nil, [][]byte{keys[2]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{t1, t2, t3})

	require.Equal(t, []TxNodeBatch{{t1}, {t2}, {t3}}, env.readForests(t))

	env.validatedTxs <- TxNodeBatch{t1, t2, t3}
	ensureProcessedAndValidatedMetrics(t, env.metrics, 3, 3)
	env.requireEmptyDetector(t)
	ensureWaitingTXsLimit(t, env.metrics, 0)
}

func TestForestLocalChainsAndJoin(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 4)

	// Chain: a <- b. Join: c, d <- e.
	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	c := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	d := createTxNode(t, nil, [][]byte{keys[2]}, nil)
	e := createTxNode(t, nil, [][]byte{keys[1], keys[2]}, nil)
	b.addDependenciesAndUpdateDependents(TxNodeBatch{a})
	e.addDependenciesAndUpdateDependents(TxNodeBatch{c, d})
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, b, c, d, e})

	forests := env.readForests(t)
	require.Len(t, forests, 2)
	requireForest(t, forests[0], a, b)
	requireForest(t, forests[1], c, d, e)

	env.validatedTxs <- TxNodeBatch{a, b, c, d, e}
	env.requireEmptyDetector(t)
	ensureWaitingTXsLimit(t, env.metrics, 0)
}

func TestForestWaitsForWholeInFlightForest(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 3)

	// a <- b. x conflicts only with a (keys[2]).
	a := createTxNode(t, nil, [][]byte{keys[0], keys[2]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b.addDependenciesAndUpdateDependents(TxNodeBatch{a})
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, b})
	forests := env.readForests(t)
	require.Len(t, forests, 1)
	requireForest(t, forests[0], a, b)

	env.validatedTxs <- TxNodeBatch{a}

	x := createTxNode(t, nil, [][]byte{keys[2]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	// x conflicts only with a, which is already validated, but its forest is still in flight.
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{b}
	require.Equal(t, []TxNodeBatch{{x}}, env.readForests(t))
	// A dependency on an in-flight forest stores no dependency edge.
	require.Empty(t, x.dependsOnTxs)

	env.validatedTxs <- TxNodeBatch{x}
	env.requireEmptyDetector(t)
}

func TestForestWaitsForAllBlockers(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 2)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	c := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, c})
	require.Equal(t, []TxNodeBatch{{a}, {c}}, env.readForests(t))

	x := createTxNode(t, nil, [][]byte{keys[0], keys[1]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{a}
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{c}
	require.Equal(t, []TxNodeBatch{{x}}, env.readForests(t))
}

func TestForestJoinsBlockedWaitingForest(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 3)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a})
	require.Equal(t, []TxNodeBatch{{a}}, env.readForests(t))

	// x waits for a. y depends only on the waiting x, across batches.
	x := createTxNode(t, nil, [][]byte{keys[0], keys[1]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	y := createTxNode(t, nil, [][]byte{keys[1], keys[2]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{y})
	env.requireNoForests(t)
	env.dm.mu.Lock()
	require.Equal(t, TxNodeBatch{x}, y.dependsOnTxs)
	env.dm.mu.Unlock()

	env.validatedTxs <- TxNodeBatch{a}
	forests := env.readForests(t)
	require.Len(t, forests, 1)
	requireForest(t, forests[0], x, y)
}

func TestForestMergeOfBlockedWaitingForests(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 4)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, b})
	require.Equal(t, []TxNodeBatch{{a}, {b}}, env.readForests(t))

	// x waits for a; y waits for b. They are separate waiting forests.
	x := createTxNode(t, nil, [][]byte{keys[0], keys[2]}, nil)
	y := createTxNode(t, nil, [][]byte{keys[1], keys[3]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x, y})
	env.requireNoForests(t)

	// z merges both waiting forests; the merged forest waits for a and b.
	z := createTxNode(t, nil, [][]byte{keys[2], keys[3]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{z})
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{a}
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{b}
	forests := env.readForests(t)
	require.Len(t, forests, 1)
	requireForest(t, forests[0], x, y, z)
	env.requireNoForests(t)
}

func TestForestWaitingTxsLimit(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 2)
	keys := makeTestKeys(t, 3)

	t1 := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	t2 := createTxNode(t, nil, [][]byte{keys[1]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{t1, t2})
	require.Equal(t, []TxNodeBatch{{t1}, {t2}}, env.readForests(t))

	t3 := createTxNode(t, nil, [][]byte{keys[2]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{t3})
	env.requireNoForests(t)
	ensureWaitingTXsLimit(t, env.metrics, 2)

	env.validatedTxs <- TxNodeBatch{t1, t2}
	require.Equal(t, []TxNodeBatch{{t3}}, env.readForests(t))
}

func TestForestIgnoresInvalidFeedback(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 1)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b.addDependenciesAndUpdateDependents(TxNodeBatch{a})
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, b})
	require.Len(t, env.readForests(t), 1)

	x := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	env.requireNoForests(t)

	rejected := NewRejectedTransactionNode(&committerpb.TxStatus{
		Ref:    committerpb.NewTxRef("rejected", 0, 0),
		Status: committerpb.Status_MALFORMED_UNSUPPORTED_ENVELOPE_PAYLOAD,
	})
	// Rejected node, duplicate of a, and x (still waiting) must all be ignored.
	env.validatedTxs <- TxNodeBatch{rejected, a, a, x}
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{b}
	require.Equal(t, []TxNodeBatch{{x}}, env.readForests(t))
}

func TestForestOutputMutationDoesNotAffectManager(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 1)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	b.addDependenciesAndUpdateDependents(TxNodeBatch{a})
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a, b})
	forests := env.readForests(t)
	require.Len(t, forests, 1)

	// The consumer owns the emitted slice and may mutate it in place.
	forests[0][0], forests[0][1] = nil, nil

	env.validatedTxs <- TxNodeBatch{a, b}
	env.requireEmptyDetector(t)

	x := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	require.Equal(t, []TxNodeBatch{{x}}, env.readForests(t))
}

func TestForestCompletedForestReleasesReferences(t *testing.T) {
	t.Parallel()
	env := newForestTestEnv(t, 20)
	keys := makeTestKeys(t, 1)

	a := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{a})
	require.Equal(t, []TxNodeBatch{{a}}, env.readForests(t))

	x := createTxNode(t, nil, [][]byte{keys[0]}, nil)
	env.incomingTxs <- createTxsNodeBatch(t, TxNodeBatch{x})
	env.requireNoForests(t)

	env.validatedTxs <- TxNodeBatch{a}
	require.Equal(t, []TxNodeBatch{{x}}, env.readForests(t))

	// A completed forest must not keep newer forests reachable.
	env.dm.mu.Lock()
	defer env.dm.mu.Unlock()
	require.Nil(t, a.forest)
}

func TestForestManagerEndToEnd(t *testing.T) {
	t.Parallel()
	incomingTxs := make(chan *TransactionBatch, 10)
	outgoing := make(chan []TxNodeBatch, 10)
	validatedTxs := make(chan TxNodeBatch, 10)
	m := NewForestManager(&ForestParameters{
		IncomingTxs:               incomingTxs,
		OutgoingDepFreeForests:    outgoing,
		IncomingValidatedTxsNode:  validatedTxs,
		NumOfLocalDepConstructors: 2,
		WaitingTxsLimit:           20,
		PrometheusMetricsProvider: monitoring.NewProvider(),
	})
	test.RunServiceForTest(t.Context(), t, func(ctx context.Context) error {
		m.Run(ctx)
		return nil
	}, nil)

	keys := makeTestKeys(t, 3)
	// Block and tx numbers are deliberately all zero: ordering must not rely on them.
	batch1 := []*servicepb.TxWithRef{
		createTxForTest(t, 0, nsID1ForTest, nil, [][]byte{keys[0]}, nil),
		createTxForTest(t, 0, nsID1ForTest, nil, [][]byte{keys[0], keys[1]}, nil),
		createTxForTest(t, 0, nsID1ForTest, nil, [][]byte{keys[2]}, nil),
	}
	incomingTxs <- &TransactionBatch{ID: 1, Txs: batch1}

	read := func() []TxNodeBatch {
		require.Eventually(t, func() bool { return len(outgoing) > 0 }, 2*time.Second, 10*time.Millisecond)
		return <-outgoing
	}

	forests := read()
	require.Len(t, forests, 2)
	require.Len(t, forests[0], 2)
	requireForest(t, forests[0], forests[0]...)
	require.Same(t, batch1[0], forests[0][0].VerifierTx)
	require.Same(t, batch1[1], forests[0][1].VerifierTx)
	require.Same(t, batch1[2], forests[1][0].VerifierTx)

	// Depends on the in-flight first forest through keys[1].
	batch2 := []*servicepb.TxWithRef{
		createTxForTest(t, 0, nsID1ForTest, [][]byte{keys[1]}, nil, nil),
	}
	incomingTxs <- &TransactionBatch{ID: 2, Txs: batch2}
	require.Never(t, func() bool { return len(outgoing) > 0 }, 500*time.Millisecond, 50*time.Millisecond)

	validatedTxs <- forests[0]
	forests2 := read()
	require.Len(t, forests2, 1)
	require.Same(t, batch2[0], forests2[0][0].VerifierTx)
}

// readForests returns the next output write.
func (env *forestTestEnv) readForests(t *testing.T) []TxNodeBatch {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(env.outgoing) > 0
	}, 2*time.Second, 10*time.Millisecond)
	return <-env.outgoing
}

// requireNoForests asserts that nothing is emitted.
func (env *forestTestEnv) requireNoForests(t *testing.T) {
	t.Helper()
	require.Never(t, func() bool {
		return len(env.outgoing) > 0
	}, 500*time.Millisecond, 50*time.Millisecond)
}

// requireEmptyDetector asserts that the detector eventually holds no transaction.
// The detector is read under the manager's lock as the manager mutates it concurrently.
func (env *forestTestEnv) requireEmptyDetector(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		env.dm.mu.Lock()
		defer env.dm.mu.Unlock()
		d := env.dm.detector
		return len(d.readOnlyKeyToWaitingTxs) == 0 && len(d.writeOnlyKeyToWaitingTxs) == 0 &&
			len(d.readWriteKeyToWaitingTxs) == 0
	}, 2*time.Second, 10*time.Millisecond)
}
