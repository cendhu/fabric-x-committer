<!--
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
-->
# Coordinator Architecture, Block Diagram, and Flow Guide

1. [Overview](#1-overview)
2. [How to Read This Document](#2-how-to-read-this-document)
3. [High-Level Block Diagram](#3-high-level-block-diagram)
4. [Coordinator Internal Block Diagram](#4-coordinator-internal-block-diagram)
5. [End-to-End Flow](#5-end-to-end-flow)
6. [Dependency Graph Construction](#6-dependency-graph-construction)
   - [6.1 Why the Graph Is Split into Local and Global Stages](#61-why-the-graph-is-split-into-local-and-global-stages)
   - [6.2 Transaction Node Model](#62-transaction-node-model)
   - [6.3 Dependency Types](#63-dependency-types)
   - [6.4 Worked Examples](#64-worked-examples)
   - [6.5 How Transactions Become Free Again](#65-how-transactions-become-free-again)
7. [Failure and Recovery](#7-failure-and-recovery)
8. [Code Map](#8-code-map)

## 1. Overview

Coordinator service is runtime orchestrator for block processing inside committer pipeline. It accepts blocks from Sidecar, builds and maintains dependency graph for in-flight transactions, dispatches dependency-free work to Verifier services, forwards all transactions to Validator-Committer services, and returns final transaction status to Sidecar.

Coordinator does **not** perform signature verification or database commit itself. Instead, it decides **when** transaction can move forward and **where** transaction should go next.

Main responsibilities:
- receive streamed blocks from Sidecar
- split valid transactions into dependency-graph batches
- send dependency-free work to Verifier pool
- forward verified and prelim-invalid transactions to Validator-Committer pool
- feed final status back into dependency graph and back to Sidecar
- track namespace/config updates so future verification requests use fresh policy state

## 2. How to Read This Document

Start with high-level block diagram if you need system shape. Then read internal coordinator block diagram to understand main components and five internal channels. Then read end-to-end flow and dependency graph examples to see why coordinator releases transactions in waves instead of strict block order.

If you want implementation entry points after building mental model, jump to [Code Map](#8-code-map). For configuration fields, gRPC API surface, and existing service overview, also see [`docs/coordinator.md`](./coordinator.md).

## 3. High-Level Block Diagram

At highest level, coordinator sits between Sidecar and two downstream service pools. Sidecar streams blocks in and receives transaction status back. Verifier services check signatures and structure. Validator-Committer services perform final validation, commit, status lookup, recovery reads, and policy/config recovery through state DB access.

```mermaid
flowchart LR
    S[Sidecar]
    C[Coordinator]
    V[Verifier Service Pool]
    VC[Validator-Committer Service Pool]
    DB[(State DB)]

    S <--> |Blocks in\nTx status batches out| C
    C <--> |Tx batches out\nVerification status back\nPolicy/config updates out| V
    C <--> |Tx batches out\nFinal tx status back| VC
    VC <--> |Commit state\nRead status / recovery state| DB
```

```text
+---------+      blocks / tx status batches      +-------------+
| Sidecar | <----------------------------------> | Coordinator |
+---------+                                      +-------------+
                                                       ^     ^
                   tx batches + policy/config deltas   |     | tx batches / final tx status
                   verification status back            |     |
                                                       |     v
                                         +-------------------------+
                                         | Verifier Service Pool   |
                                         +-------------------------+

                                         +------------------------------+
                                         | Validator-Committer Pool     |
                                         +------------------------------+
                                                       ^
                                                       | commit state /
                                                       | read status + recovery state
                                                       v
                                                 +-----------+
                                                 | State DB  |
                                                 +-----------+
```

Each bidirectional edge carries different traffic:
- **Sidecar ↔ Coordinator:** Sidecar sends blocks over `BlockProcessing`; Coordinator sends `TxStatusBatch` messages back.
- **Coordinator ↔ Verifier:** Coordinator sends dependency-free transaction batches plus any pending policy/config deltas; Verifier services send verification status back.
- **Coordinator ↔ Validator-Committer:** Coordinator sends transaction batches for final validation/commit; Validator-Committer services send final status back.
- **Validator-Committer ↔ State DB:** Validator-Committer writes commit results and reads persisted status, last committed block, namespace policies, and config transaction during recovery.

## 4. Coordinator Internal Block Diagram

Inside coordinator, control is split across five main components:
- `Service` owns external gRPC surface and stream lifecycle.
- `dependencygraph.Manager` decides which transactions are free to run.
- `signatureVerifierManager` talks to Verifier service pool.
- `validatorCommitterManager` talks to Validator-Committer pool and sends final results to two consumers.
- `policyManager` stores latest namespace/config state for future verifier requests.

Runtime path mostly moves forward. Two feedback loops matter most:
1. final status flows from Validator-Committer manager back to dependency graph so blocked dependents can be released
2. committed namespace/config changes flow into policy manager so later verifier requests carry fresh policy/config deltas

```mermaid
flowchart LR
    subgraph C[Coordinator]
        SVC[Service]
        LDC[local dependency constructor]
        GDM[global dependency manager]
        SVM[signatureVerifierManager]
        VCM[validatorCommitterManager]
        PM[policyManager]

        SVC -->|coordinatorToDepGraphTxs| LDC
        LDC -->|local dependency output| GDM
        GDM -->|depGraphToSigVerifierFreeTxs| SVM
        SVM -->|sigVerifierToVCServiceValidatedTxs| VCM
        VCM -->|vcServiceToDepGraphValidatedTxs| GDM
        VCM -->|vcServiceToCoordinatorTxStatus| SVC
        VCM -->|committed namespace/config tx updates| PM
        PM -->|policy/config deltas for next verifier request| SVM
    end
```

```text
Service
  |
  | coordinatorToDepGraphTxs
  v
Dependency Graph Manager
  |-- local dependency constructor
  `-- global dependency manager
         |
         | depGraphToSigVerifierFreeTxs
         v
Signature Verifier Manager <---- policy/config deltas ---- Policy Manager
         |
         | sigVerifierToVCServiceValidatedTxs
         v
Validator-Committer Manager ---- committed namespace/config txs ---> Policy Manager
         | \
         |  `---- vcServiceToCoordinatorTxStatus ----> Service
         |
         `------- vcServiceToDepGraphValidatedTxs ---> Dependency Graph Manager
```

### Channel walkthrough

| Channel | Producer | Consumer | Meaning |
|---|---|---|---|
| `coordinatorToDepGraphTxs` | `Service` | dependency graph manager | Valid transactions received from Sidecar, chunked into batches for dependency construction |
| `depGraphToSigVerifierFreeTxs` | global dependency manager | signature verifier manager | Transactions with no remaining dependencies |
| `sigVerifierToVCServiceValidatedTxs` | signature verifier manager | validator-committer manager | Transactions after signature/structural verification; prelim-invalid transactions still continue |
| `vcServiceToDepGraphValidatedTxs` | validator-committer manager | global dependency manager | Finalized transaction nodes used to remove edges and free dependents |
| `vcServiceToCoordinatorTxStatus` | validator-committer manager | `Service` | Final `TxStatusBatch` messages returned to Sidecar |

Component ownership in plain terms:
- `Service` handles stream send/receive and counts waiting transactions.
- local dependency constructor computes **within-batch** dependencies in parallel.
- global dependency manager merges local results with already waiting transactions and tracks release of dependents.
- signature verifier manager retries verifier streams and requeues in-flight transactions on failure.
- validator-committer manager retries VC streams, fans status out to two destinations, and updates policy manager before dependents advance.
- policy manager versions namespace/config state and hands verifier managers only deltas they have not seen yet.

## 5. End-to-End Flow

### Happy path

1. Sidecar opens `BlockProcessing` bidirectional stream to Coordinator.
2. Sidecar sends block containing `Txs` and already rejected transactions.
3. Coordinator increments waiting counters and splits valid transactions into dependency-graph batches.
4. Coordinator wraps already rejected transactions as rejected transaction nodes and sends them directly toward Validator-Committer path.
5. Local dependency constructors compute within-batch dependencies in parallel.
6. Global dependency manager merges those batches into global waiting graph and emits only dependency-free transactions.
7. Signature verifier manager fetches any pending policy/config deltas from `policyManager` and sends dependency-free transactions to Verifier pool.
8. Verifier services return status. If transaction fails signature or structural checks, Coordinator records prelim-invalid status on node, but still forwards node downstream.
9. Validator-Committer manager sends all transactions to Validator-Committer pool for final validation/commit or final recording of already-invalid transactions.
10. When VC services return final status, validator-committer manager sends status in two directions: back to dependency graph and back to `Service`.
11. If committed transaction changes namespace policy or config, validator-committer manager updates `policyManager` **before** dependents are released.
12. Service streams final `TxStatusBatch` messages back to Sidecar, which aggregates status at block level and serves clients.

```mermaid
sequenceDiagram
    participant S as Sidecar
    participant C as Coordinator Service
    participant D as Dependency Graph
    participant V as Verifier Manager/Pool
    participant VC as Validator-Committer Manager/Pool
    participant P as Policy Manager

    S->>C: BlockProcessing stream sends block
    C->>D: Valid tx batches
    C->>VC: Rejected tx nodes with prelim invalid status
    D->>V: Dependency-free tx nodes
    P-->>V: Policy/config deltas for next request
    V-->>VC: Verified tx nodes (valid and prelim-invalid)
    VC->>P: Committed namespace/config transaction updates
    VC-->>D: Validated tx nodes free dependents
    VC-->>C: Final TxStatusBatch messages
    C-->>S: Tx status batches
```

```text
Sidecar
  |
  | block stream
  v
Coordinator Service
  |-- valid txs ----------------------> Dependency Graph
  |-- rejected txs -------------------> Validator-Committer path

Dependency Graph
  |-- dependency-free txs -----------> Signature Verifier Manager

Policy Manager
  `-- pending policy/config deltas --> Signature Verifier Manager

Signature Verifier Manager
  `-- all verified tx nodes ---------> Validator-Committer Manager

Validator-Committer Manager
  |-- final status ------------------> Coordinator Service
  |-- validated tx nodes -----------> Dependency Graph
  `-- committed namespace/config ---> Policy Manager

Coordinator Service
  `-- Tx status batches ------------> Sidecar
```

> **Rejected transaction fast path:** Transactions already marked rejected by Sidecar do not enter dependency graph. Coordinator wraps them as rejected transaction nodes and forwards them directly to validator-committer path so final status still reaches persistent storage and normal return channel.

Why prelim-invalid transactions still continue downstream:
- final status must still be recorded in persistent state
- downstream status return path stays uniform
- Sidecar still receives one final status stream for all transactions in block

## 6. Dependency Graph Construction

Dependency graph is coordinator feature that unlocks parallelism without losing deterministic outcomes. Later transactions may have to wait for earlier transactions if both touch same logical key or same namespace lifecycle state.

### 6.1 Why the Graph Is Split into Local and Global Stages

Coordinator splits dependency work into two stages because it needs both throughput and ordering discipline.

**Local stage** (`local_dependency_constructor.go`):
- receives one transaction batch at time
- computes dependencies **inside that batch only**
- runs multiple workers in parallel
- preserves output order using batch IDs and condition-variable gating, so global stage still sees batches in original order

**Global stage** (`global_dependency_manager.go`):
- receives locally processed batches in original order
- detects dependencies against transactions already waiting from earlier batches
- merges new read/write index data into global detector
- emits only transactions with zero remaining dependencies
- removes completed transactions and releases newly free dependents

Short version:
- local stage = fast within-batch preprocessing
- global stage = authoritative waiting graph across all in-flight transactions

### 6.2 Transaction Node Model

Coordinator does not track raw transactions directly in graph. It wraps each transaction in `TransactionNode`.

Each node carries:
- transaction reference and namespace payload (`Tx`)
- endorsements needed by verifier stage
- `dependsOnTxs`: coarse-grained set of earlier transactions that must finish first
- `dependentTxs`: reverse edges used when freeing blocked transactions
- extracted read/write key sets for dependency detection

Coordinator intentionally tracks **coarse-grained dependency edges** instead of labeling each stored edge as read-write, write-read, or write-write. That keeps graph logic simpler. Fine-grained type still matters conceptually for humans, but runtime release logic only needs to know whether dependency still exists.

Read/write extraction rules matter:
- read-only keys go into read set
- blind writes go into write-only set
- read-write operations go into read-write set
- normal transactions also implicitly read namespace lifecycle key from meta-namespace so namespace policy/config changes serialize correctly with regular namespace traffic

That last rule prevents namespace lifecycle transaction from racing with ordinary state updates in same namespace.

### 6.3 Dependency Types

| Type | Meaning | Example | Why it matters |
|---|---|---|---|
| read-write | later transaction reads key written by earlier transaction | `T1` writes `ns1:a`, `T2` reads `ns1:a` | `T2` may have read stale value if `T1` commits, so `T2` must wait |
| write-read | later transaction writes key read by earlier transaction | `T1` reads `ns1:a`, `T2` writes `ns1:a` | preserves block-order semantics so later write does not jump ahead of earlier read |
| write-write | both transactions write same key | `T1` writes `ns1:a`, `T2` writes `ns1:a` | prevents later write from overtaking earlier write |

Important direction rule:
- edge always points from later transaction to earlier transaction it depends on
- because dependencies only point backward in stream/block order, graph stays acyclic

### 6.4 Worked Examples

#### Example 1: Independent transactions

| Tx | Reads | Writes |
|---|---|---|
| `T1` | `ns1:a` | - |
| `T2` | `ns1:b` | `ns1:c` |

No shared keys. No namespace-lifecycle interaction. Both transactions are dependency-free immediately.

```text
T1      T2
|       |
free    free
```

Result:
- `T1` and `T2` can both move to Verifier stage immediately
- dependency graph does not need to serialize them

#### Example 2: Later read depends on earlier write

| Tx | Reads | Writes |
|---|---|---|
| `T1` | - | `ns1:a` |
| `T2` | `ns1:a` | - |

`T2` has read-write dependency on `T1`.

```text
T1 ---> T2
free    waits
```

Result:
- `T1` is free first
- `T2` waits in graph
- after VC finalizes `T1`, graph removes edge and `T2` becomes free

#### Example 3: Two writes to same key across different batches

Suppose first batch already contains `T1`, and second batch later introduces `T2`.

| Tx | Batch | Reads | Writes |
|---|---|---|---|
| `T1` | `B1` | - | `ns1:a` |
| `T2` | `B2` | - | `ns1:a` |

`T2` has write-write dependency on `T1`, even though they arrived in different batches.

```text
Batch B1:  T1
             \
              ---> T2  : Batch B2
```

Result:
- local stage alone cannot resolve this because transactions are in different batches
- global stage detects dependency using waiting transaction index
- `T2` stays blocked until `T1` is finalized

#### Example 4: Namespace lifecycle transaction must serialize with normal traffic

Suppose `T1` updates regular state in `ns1`, and `T2` changes namespace policy/config for `ns1`.

| Tx | Reads | Writes |
|---|---|---|
| `T1` | implicit meta read for `ns1` | `ns1:asset1` |
| `T2` | meta/config data | namespace lifecycle update for `ns1` |

Coordinator treats normal transaction as reading namespace lifecycle key in meta-namespace. That creates ordering edge between normal traffic and policy/config lifecycle traffic.

```text
T1 ----> T2
state    namespace policy/config update
```

And for later normal traffic:

```text
T2 ----> T3
policy   later normal tx in same namespace
update
```

Result:
- lifecycle update cannot jump ahead of already waiting normal transaction in same namespace
- later normal transactions cannot verify against stale policy state
- once `T2` commits, policy manager updates before newly freed dependents move forward

### 6.5 How Transactions Become Free Again

Freeing is driven by final status from Validator-Committer stage, not by verifier completion.

Release loop:
1. Validator-Committer manager receives final status from VC service.
2. It maps returned status back to stored `TransactionNode`.
3. It updates `policyManager` first if committed transaction changed namespace/config state.
4. It sends finalized nodes to global dependency manager over `vcServiceToDepGraphValidatedTxs`.
5. Global dependency manager removes completed transaction read/write keys from dependency detector.
6. For each dependent transaction, graph removes one edge from `dependsOnTxs`.
7. Any dependent that now has zero remaining dependencies becomes free.
8. Newly freed transactions are emitted to verifier manager over `depGraphToSigVerifierFreeTxs`.

```text
VC final status
   |
   v
remove finished node from graph
   |
   v
update dependent nodes
   |
   +--> still has dependencies -> keep waiting
   |
   `--> zero dependencies -> emit to Verifier stage
```

## 7. Failure and Recovery

Coordinator is designed to survive stream breaks, worker-service failures, and process restarts. Critical theme: in-flight work may be retried, but final state remains correct because VC path is idempotent and duplicate late responses are tolerated.

### 7.1 Sidecar Stream Ends Mid-Flight

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| `BlockProcessing` stream between Sidecar and Coordinator ends | coordinator managers, internal queues, and already received transaction batches may still exist | Sidecar can reconnect and resume later block delivery | VC-persisted transaction status and last committed block data stay in DB | block processing is decoupled from stream after receipt; received work can continue downstream |

Key detail:
- stream-specific goroutines end with stream
- but blocks already received by coordinator may still be forwarded and processed
- status may accumulate in coordinator queue even if stream itself has ended

### 7.2 Signature Verifier Failure

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| verifier stream or verifier server fails | `signatureVerifier.txBeingValidated` still tracks in-flight nodes for that verifier instance | manager reconnects using sustained retry loop and requeues pending transactions from `txBeingValidated` | nothing new must persist at verifier stage | verifier stage is retry-safe because transactions are re-enqueued and revalidated |

Key detail:
- verifier manager stores in-flight nodes by transaction height
- on failure, `recoverPendingTransactions()` pushes them back into input queue
- later verifier stream can resend them with latest policy/config deltas

### 7.3 Validator-Committer Failure

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| VC stream or VC server fails | `validatorCommitter.txBeingValidated` still tracks in-flight nodes for that VC instance | manager reconnects using sustained retry loop and requeues pending transactions from `txBeingValidated` | any transaction already committed by VC stays in DB | replay is safe because VC path can detect already committed transaction status |

Key detail:
- VC manager tracks in-flight nodes by transaction ID
- if stream fails, `recoverPendingTransactions()` requeues nodes
- some of those transactions may already have committed before failure; duplicate replay is tolerated

### 7.4 Duplicate or Late Status After Reconnect

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| coordinator receives delayed or duplicate VC response after retry/reconnect | first successful lookup removes matching node from `txBeingValidated` | duplicate response is effectively ignored because lookup no longer finds tracked node | committed status already stored in DB | same transaction can be submitted more than once, but only first tracked response updates in-memory release path |

Key detail:
- `getTxsAndUpdatePolicies()` loads and deletes tracked node once
- if later duplicate status arrives, node is no longer in `txBeingValidated`
- duplicate status is dropped from status batch before downstream processing

### 7.5 Coordinator Restart and Recovery

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| coordinator process restarts | in-memory graph and queues are lost | coordinator rebuilds runtime state from startup recovery calls and new Sidecar delivery | last committed block, transaction status, namespace policies, and config transaction stay in DB | replay after restart is safe because VC commit path is idempotent and returns existing status for already processed transaction |

Restart flow:
1. validator-committer manager becomes ready
2. coordinator recovers namespace policies and latest config transaction through VC common client
3. policy manager is rebuilt before coordinator signals ready
4. Sidecar asks which block should come next and resumes delivery
5. replayed transactions are safe because VC can return existing status instead of re-committing

### 7.6 Policy Reconstruction on Startup

| Failure point | What stays in memory | What is retried or rebuilt | What persists | Why result stays correct |
|---|---|---|---|---|
| coordinator starts with empty in-memory policy manager | empty policy manager exists only temporarily | startup recovery reads persisted namespace policies and config transaction, then rebuilds policy manager versions | namespace policies and config transaction are stored in DB through VC path | verifier requests after readiness always see recovered policy/config baseline, then only later deltas |

Key detail:
- startup calls `recoverPolicyManagerFromStateDB()` before coordinator signals ready
- policy manager stores versions internally and returns only deltas newer than verifier has already seen
- this avoids pushing full policy snapshot on every verifier request once startup baseline is loaded

## 8. Code Map

Open these files next if you want to connect diagrams back to implementation:
- `service/coordinator/coordinator.go` — external gRPC service, stream handling, channel wiring, and coordinator startup lifecycle.
- `service/coordinator/dependencygraph/manager.go` — top-level dependency graph module that runs local and global stages.
- `service/coordinator/dependencygraph/local_dependency_constructor.go` — within-batch dependency construction and batch-order preservation logic.
- `service/coordinator/dependencygraph/global_dependency_manager.go` — waiting-graph maintenance, release of freed dependents, and output of dependency-free work.
- `service/coordinator/dependencygraph/dependency_detector.go` — read/write index structure that detects read-write, write-read, and write-write relationships.
- `service/coordinator/dependencygraph/transaction_node.go` — node model, dependency sets, and namespace lifecycle key treatment.
- `service/coordinator/signature_verifier_manager.go` — verifier stream management, pending-policy fetch, retry, and requeue of in-flight verification work.
- `service/coordinator/validator_committer_manager.go` — VC stream management, final-status fan-out, policy updates, and duplicate-response handling.
- `service/coordinator/policy_manager.go` — in-memory versioned store for namespace policies and config transaction updates.
