<!--
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
-->
# gRPC API Reference

This document provides a comprehensive reference for all gRPC APIs exposed by the Fabric-X Committer microservices.

## Table of Contents

- [API Summary](#api-summary)
1. [Sidecar Service](#1-sidecar-service)
2. [Coordinator Service](#2-coordinator-service)
3. [Verifier Service](#3-verifier-service)
4. [Validator-Committer Service](#4-validator-committer-service)
5. [Query Service](#5-query-service)
6. [Common Message Types](#6-common-message-types)
7. [Status Codes](#7-status-codes)

---

## API Summary

This section provides a quick reference of all gRPC APIs. Click on any API name for detailed documentation.

### Sidecar Service

| Service | API | Description |
|---------|-----|-------------|
| `peer.Deliver` | [Deliver](#111-deliver) | Stream blocks from the ledger |
| `peer.Deliver` | [DeliverFiltered](#112-deliverfiltered) | Deprecated |
| `peer.Deliver` | [DeliverWithPrivateData](#113-deliverwithprivatedata) | Deprecated |
| `committerpb.Notifier` | [OpenNotificationStream](#121-opennotificationstream) | Subscribe to transaction status notifications |

### Coordinator Service

| Service | API | Description |
|---------|-----|-------------|
| `servicepb.Coordinator` | [BlockProcessing](#21-blockprocessing) | Process transaction batches from blocks |
| `servicepb.Coordinator` | [SetLastCommittedBlockNumber](#22-setlastcommittedblocknumber) | Update last committed block for recovery |
| `servicepb.Coordinator` | [GetNextBlockNumberToCommit](#23-getnextblocknumbertocommit) | Get next block to process |
| `servicepb.Coordinator` | [GetTransactionsStatus](#24-gettransactionsstatus) | Query transaction statuses by ID |
| `servicepb.Coordinator` | [GetConfigTransaction](#25-getconfigtransaction) | Get system configuration |
| `servicepb.Coordinator` | [NumberOfWaitingTransactionsForStatus](#26-numberofwaitingtransactionsforstatus) | Get pipeline queue depth |

### Verifier Service

| Service | API | Description |
|---------|-----|-------------|
| `servicepb.Verifier` | [StartStream](#31-startstream) | Verify transaction signatures |

### Validator-Committer Service

| Service | API | Description |
|---------|-----|-------------|
| `servicepb.ValidationAndCommitService` | [StartValidateAndCommitStream](#41-startvalidateandcommitstream) | Validate and commit transactions |
| `servicepb.ValidationAndCommitService` | [SetLastCommittedBlockNumber](#42-setlastcommittedblocknumber-1) | Store last committed block |
| `servicepb.ValidationAndCommitService` | [GetNextBlockNumberToCommit](#43-getnextblocknumbertocommit-1) | Get next block to commit |
| `servicepb.ValidationAndCommitService` | [GetTransactionsStatus](#44-gettransactionsstatus-1) | Query transaction statuses |
| `servicepb.ValidationAndCommitService` | [GetNamespacePolicies](#45-getnamespacepolicies) | Get endorsement policies |
| `servicepb.ValidationAndCommitService` | [GetConfigTransaction](#46-getconfigtransaction-1) | Get system configuration |
| `servicepb.ValidationAndCommitService` | [SetupSystemTablesAndNamespaces](#47-setupsystemtablesandnamespaces) | Initialize database |

### Query Service

| Service | API | Description |
|---------|-----|-------------|
| `committerpb.QueryService` | [BeginView](#51-beginview) | Create database view with isolation level |
| `committerpb.QueryService` | [EndView](#52-endview) | Close database view |
| `committerpb.QueryService` | [GetRows](#53-getrows) | Read key-value pairs |
| `committerpb.QueryService` | [GetNamespacePolicies](#54-getnamespacepolicies-1) | Get endorsement policies |
| `committerpb.QueryService` | [GetConfigTransaction](#55-getconfigtransaction-2) | Get system configuration |
| `committerpb.QueryService` | [GetTransactionStatus](#56-gettransactionstatus) | Query transaction status |

---

## 1. Sidecar Service

The Sidecar service acts as middleware between the Ordering Service and Coordinator. It exposes two gRPC services for external clients: a block delivery service and a transaction notification service.

### 1.1 Deliver Service

**Proto file:** `peer/events.proto` (from fabric-protos-go-apiv2)
**Package:** `peer`
**Service:** `Deliver`

The Deliver service allows clients to retrieve committed blocks from the Sidecar's ledger store.

#### 1.1.1 Deliver

```protobuf
rpc Deliver(stream Envelope) returns (stream DeliverResponse);
```

**Description:** Bidirectional streaming RPC for block delivery. Clients send seek requests specifying which blocks they want, and the Sidecar streams back the requested blocks.

**Input: `Envelope`** (from `common` package)
| Field | Type | Description |
|-------|------|-------------|
| `payload` | `bytes` | Serialized `Payload` containing a `SeekInfo` request |
| `signature` | `bytes` | Signature over the payload |

**Expanded: `SeekInfo`** (from `orderer` package)
| Field | Type | Description |
|-------|------|-------------|
| `start` | `SeekPosition` | Starting position for block delivery |
| `stop` | `SeekPosition` | Stopping position for block delivery |
| `behavior` | `SeekBehavior` | Behavior when block is not available |

**Expanded: `SeekPosition`**
| Field | Type | Description |
|-------|------|-------------|
| `oldest` | `SeekOldest` | (oneof) Request the oldest block |
| `newest` | `SeekNewest` | (oneof) Request the newest block |
| `specified` | `SeekSpecified` | (oneof) Request a specific block number |

**Expanded: `SeekSpecified`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | The specific block number to seek |

**Output: `DeliverResponse`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `Status` | (oneof) Delivery status code |
| `block` | `Block` | (oneof) The delivered block |

**Expanded: `Block`** (from `common` package)
| Field | Type | Description |
|-------|------|-------------|
| `header` | `BlockHeader` | Block header with number, previous hash, data hash |
| `data` | `BlockData` | Block data containing transactions |
| `metadata` | `BlockMetadata` | Block metadata |

**Expanded: `BlockHeader`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | Block number |
| `previous_hash` | `bytes` | Hash of the previous block header |
| `data_hash` | `bytes` | Hash of the block data |

**Example:**

```go
stream, _ := client.Deliver(ctx)

// Request blocks 10-20
seekInfo := &orderer.SeekInfo{
    Start: &orderer.SeekPosition{Type: &orderer.SeekPosition_Specified{
        Specified: &orderer.SeekSpecified{Number: 10},
    }},
    Stop: &orderer.SeekPosition{Type: &orderer.SeekPosition_Specified{
        Specified: &orderer.SeekSpecified{Number: 20},
    }},
}
stream.Send(createSignedEnvelope(seekInfo))

for {
    resp, err := stream.Recv()
    if err == io.EOF { break }
    if block := resp.GetBlock(); block != nil {
        fmt.Printf("Block %d\n", block.Header.Number)
    }
}
```

---

#### 1.1.2 DeliverFiltered

```protobuf
rpc DeliverFiltered(stream Envelope) returns (stream DeliverResponse);
```

**Description:** Deprecated. Returns `UNIMPLEMENTED` error. Exists for Fabric SDK compatibility.

---

#### 1.1.3 DeliverWithPrivateData

```protobuf
rpc DeliverWithPrivateData(stream Envelope) returns (stream DeliverResponse);
```

**Description:** Deprecated. Returns `UNIMPLEMENTED` error. Exists for Fabric SDK compatibility.

---

### 1.2 Notifier Service

**Proto file:** `api/committerpb/notify.proto` (from fabric-x-common)
**Package:** `committerpb`
**Service:** `Notifier`

The Notifier service allows clients to subscribe to transaction status events and receive asynchronous notifications when transactions are committed or aborted.

#### 1.2.1 OpenNotificationStream

```protobuf
rpc OpenNotificationStream(stream NotificationRequest) returns (stream NotificationResponse);
```

**Description:** Bidirectional streaming RPC for transaction status notifications. Clients subscribe to specific transaction IDs and receive notifications when those transactions reach a final state (committed or aborted) or when the subscription times out.

**Input: `NotificationRequest`**
| Field | Type | Description |
|-------|------|-------------|
| `tx_status_request` | `optional TxIDsBatch` | Transaction IDs to subscribe to |
| `timeout` | `google.protobuf.Duration` | Timeout for this subscription (0 = use default) |

**Expanded: `TxIDsBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `tx_ids` | `repeated string` | List of transaction IDs to subscribe to |

**Output: `NotificationResponse`**
| Field | Type | Description |
|-------|------|-------------|
| `tx_status_events` | `repeated TxStatus` | Transaction status events (committed/aborted) |
| `timeout_tx_ids` | `repeated string` | Transaction IDs that timed out waiting for status |

**Expanded: `TxStatus`**
| Field | Type | Description |
|-------|------|-------------|
| `ref` | `TxRef` | Transaction reference |
| `status` | `Status` | Final transaction status |

**Example:**

```go
stream, _ := client.OpenNotificationStream(ctx)

// Subscribe to transaction statuses with 30s timeout
stream.Send(&committerpb.NotificationRequest{
    TxStatusRequest: &committerpb.TxIDsBatch{TxIds: []string{"tx-abc-123", "tx-def-456"}},
    Timeout:         durationpb.New(30 * time.Second),
})

for {
    resp, err := stream.Recv()
    if err != nil { break }
    for _, s := range resp.TxStatusEvents {
        fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
    }
}
```

---

## 2. Coordinator Service

**Proto file:** `api/servicepb/coordinator.proto`
**Package:** `servicepb`
**Service:** `Coordinator`

The Coordinator service orchestrates the transaction validation and commit pipeline. It receives blocks from the Sidecar and manages the flow through signature verification and final commitment.

### 2.1 BlockProcessing

```protobuf
rpc BlockProcessing(stream CoordinatorBatch) returns (stream TxStatusBatch);
```

**Description:** Bidirectional streaming RPC for processing blocks. The Sidecar streams transaction batches to the Coordinator, which processes them through the validation pipeline and streams back transaction statuses.

**Input: `CoordinatorBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `txs` | `repeated TxWithRef` | List of transactions to process |
| `rejected` | `repeated TxStatus` | Transactions already rejected by the Sidecar (e.g., duplicates) |

**Expanded: `TxWithRef`**
| Field | Type | Description |
|-------|------|-------------|
| `ref` | `TxRef` | Transaction reference |
| `content` | `Tx` | Full transaction content |

**Output: `TxStatusBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Final status for each processed transaction |

**Example:**

```go
stream, _ := client.BlockProcessing(ctx)

// Send transactions from a block
stream.Send(&servicepb.CoordinatorBatch{
    Txs: []*servicepb.TxWithRef{
        {Ref: &committerpb.TxRef{BlockNum: 100, TxNum: 0, TxId: "tx-001"}, Content: tx1},
        {Ref: &committerpb.TxRef{BlockNum: 100, TxNum: 1, TxId: "tx-002"}, Content: tx2},
    },
})

for {
    batch, err := stream.Recv()
    if err != nil { break }
    for _, s := range batch.Status {
        fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
    }
}
```

---

### 2.2 SetLastCommittedBlockNumber

```protobuf
rpc SetLastCommittedBlockNumber(BlockRef) returns (google.protobuf.Empty);
```

**Description:** Informs the Coordinator of the latest block number that has been successfully and sequentially committed. Used for state synchronization and recovery.

**Input: `BlockRef`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | The block number that was committed |

**Output:** Empty response on success.

**Example:**

```go
_, err := client.SetLastCommittedBlockNumber(ctx, &servicepb.BlockRef{Number: 1000})
```

---

### 2.3 GetNextBlockNumberToCommit

```protobuf
rpc GetNextBlockNumberToCommit(google.protobuf.Empty) returns (BlockRef);
```

**Description:** Returns the next block number that should be fetched and processed. Used by the Sidecar on startup/recovery to determine where to resume.

**Input:** Empty request.

**Output: `BlockRef`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | The next expected block number |

**Example:**

```go
blockRef, _ := client.GetNextBlockNumberToCommit(ctx, &emptypb.Empty{})
fmt.Printf("Next block to commit: %d\n", blockRef.Number)
```

---

### 2.4 GetTransactionsStatus

```protobuf
rpc GetTransactionsStatus(TxIDsBatch) returns (TxStatusBatch);
```

**Description:** Queries the final status of specific transactions by their IDs. The Coordinator forwards this query to a Validator-Committer service.

**Input: `TxIDsBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `tx_ids` | `repeated string` | List of transaction IDs to query |

**Output: `TxStatusBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Status for each queried transaction |

**Example:**

```go
resp, _ := client.GetTransactionsStatus(ctx, &committerpb.TxIDsBatch{
    TxIds: []string{"tx-abc-123", "tx-def-456"},
})
for _, s := range resp.Status {
    fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
}
```

---

### 2.5 GetConfigTransaction

```protobuf
rpc GetConfigTransaction(google.protobuf.Empty) returns (ConfigTransaction);
```

**Description:** Retrieves the latest system configuration transaction from the database.

**Input:** Empty request.

**Output: `ConfigTransaction`**
| Field | Type | Description |
|-------|------|-------------|
| `envelope` | `bytes` | Serialized configuration envelope |
| `version` | `uint64` | Configuration version number |

**Example:**

```go
config, _ := client.GetConfigTransaction(ctx, &emptypb.Empty{})
fmt.Printf("Config version: %d\n", config.Version)
```

---

### 2.6 NumberOfWaitingTransactionsForStatus

```protobuf
rpc NumberOfWaitingTransactionsForStatus(google.protobuf.Empty) returns (WaitingTransactions);
```

**Description:** Returns the count of transactions currently in the processing pipeline awaiting final status. Useful for monitoring and backpressure.

**Input:** Empty request.

**Output: `WaitingTransactions`**
| Field | Type | Description |
|-------|------|-------------|
| `count` | `int32` | Number of transactions awaiting status |

**Example:**

```go
waiting, _ := client.NumberOfWaitingTransactionsForStatus(ctx, &emptypb.Empty{})
fmt.Printf("Transactions waiting: %d\n", waiting.Count)
```

---

## 3. Verifier Service

**Proto file:** `api/servicepb/verifier.proto`
**Package:** `servicepb`
**Service:** `Verifier`

The Verifier service validates transaction signatures against namespace endorsement policies. It performs structural validation and signature checks before transactions proceed to final commitment.

### 3.1 StartStream

```protobuf
rpc StartStream(stream VerifierBatch) returns (stream TxStatusBatch);
```

**Description:** Bidirectional streaming RPC for signature verification. The Coordinator streams transaction batches along with policy updates, and receives verification results.

**Input: `VerifierBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `update` | `optional VerifierUpdates` | Configuration and policy updates to apply before processing |
| `requests` | `repeated TxWithRef` | Transactions to verify |

**Expanded: `VerifierUpdates`**
| Field | Type | Description |
|-------|------|-------------|
| `config` | `optional ConfigTransaction` | Channel configuration update |
| `namespace_policies` | `optional NamespacePolicies` | Endorsement policy updates |

**Expanded: `TxWithRef`**
| Field | Type | Description |
|-------|------|-------------|
| `ref` | `TxRef` | Transaction reference |
| `content` | `Tx` | Full transaction content |

**Expanded: `TxRef`**
| Field | Type | Description |
|-------|------|-------------|
| `block_num` | `uint64` | Block number |
| `tx_num` | `uint32` | Transaction index within the block |
| `tx_id` | `string` | Transaction ID |

**Expanded: `Tx`**
| Field | Type | Description |
|-------|------|-------------|
| `namespaces` | `repeated TxNamespace` | Namespaces accessed by the transaction |
| `endorsements` | `repeated Endorsements` | Endorsement signatures (one per namespace) |

**Expanded: `Endorsements`**
| Field | Type | Description |
|-------|------|-------------|
| `endorsements_with_identity` | `repeated EndorsementWithIdentity` | List of signatures with identities |

**Expanded: `EndorsementWithIdentity`**
| Field | Type | Description |
|-------|------|-------------|
| `endorsement` | `bytes` | Cryptographic signature bytes |
| `identity` | `Identity` | Identity of the signer |

**Expanded: `Identity`**
| Field | Type | Description |
|-------|------|-------------|
| `msp_id` | `string` | Membership Service Provider ID |
| `certificate` | `bytes` | (oneof) Raw certificate bytes |
| `certificate_id` | `string` | (oneof) Pre-stored certificate identifier |

**Output: `TxStatusBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Verification result for each transaction |

**Example:**

```go
stream, _ := client.StartStream(ctx)

// Send transactions with policy updates
stream.Send(&servicepb.VerifierBatch{
    Update: &servicepb.VerifierUpdates{NamespacePolicies: policies},
    Requests: []*servicepb.TxWithRef{
        {Ref: &committerpb.TxRef{BlockNum: 100, TxNum: 0, TxId: "tx-001"}, Content: tx},
    },
})

for {
    batch, err := stream.Recv()
    if err != nil { break }
    for _, s := range batch.Status {
        fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
    }
}
```

---

## 4. Validator-Committer Service

**Proto file:** `api/servicepb/vcservice.proto`
**Package:** `servicepb`
**Service:** `ValidationAndCommitService`

The Validator-Committer service performs final transaction processing with optimistic concurrency control. It validates read-sets against current state and commits write-sets to the database.

### 4.1 StartValidateAndCommitStream

```protobuf
rpc StartValidateAndCommitStream(stream VcBatch) returns (stream TxStatusBatch);
```

**Description:** Bidirectional streaming RPC for validation and commit. The Coordinator streams transaction batches, which are validated for MVCC conflicts and committed to the database.

**Input: `VcBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `transactions` | `repeated VcTx` | Transactions to validate and commit |

**Expanded: `VcTx`**
| Field | Type | Description |
|-------|------|-------------|
| `ref` | `TxRef` | Transaction reference (block number, tx index, tx ID) |
| `namespaces` | `repeated TxNamespace` | Namespaces accessed by the transaction |
| `prelim_invalid_tx_status` | `optional Status` | Pre-set invalid status from signature verification (if failed) |

**Expanded: `TxNamespace`**
| Field | Type | Description |
|-------|------|-------------|
| `ns_id` | `string` | Namespace identifier |
| `ns_version` | `uint64` | Version of the namespace |
| `reads_only` | `repeated Read` | Read-only operations |
| `read_writes` | `repeated ReadWrite` | Read-write operations |
| `blind_writes` | `repeated Write` | Blind write operations |

**Expanded: `Read`**
| Field | Type | Description |
|-------|------|-------------|
| `key` | `bytes` | The key being read |
| `version` | `optional uint64` | Version of the key (nil = doesn't exist) |

**Expanded: `ReadWrite`**
| Field | Type | Description |
|-------|------|-------------|
| `key` | `bytes` | The key being read and written |
| `version` | `optional uint64` | Version of the key being read (nil = doesn't exist) |
| `value` | `bytes` | The value to write |

**Expanded: `Write`**
| Field | Type | Description |
|-------|------|-------------|
| `key` | `bytes` | The key being written |
| `value` | `bytes` | The value to write |

**Output: `TxStatusBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Final status for each transaction |

**Example:**

```go
stream, _ := client.StartValidateAndCommitStream(ctx)

stream.Send(&servicepb.VcBatch{
    Transactions: []*servicepb.VcTx{
        {Ref: &committerpb.TxRef{BlockNum: 100, TxNum: 0, TxId: "tx-001"}, Namespaces: namespaces},
    },
})

for {
    batch, err := stream.Recv()
    if err != nil { break }
    for _, s := range batch.Status {
        fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
    }
}
```

---

### 4.2 SetLastCommittedBlockNumber

```protobuf
rpc SetLastCommittedBlockNumber(BlockRef) returns (google.protobuf.Empty);
```

**Description:** Stores the last committed block number in the state database for recovery purposes.

**Input: `BlockRef`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | The block number that was committed |

**Output:** Empty response on success.

**Example:**

```go
_, err := client.SetLastCommittedBlockNumber(ctx, &servicepb.BlockRef{Number: 1000})
```

---

### 4.3 GetNextBlockNumberToCommit

```protobuf
rpc GetNextBlockNumberToCommit(google.protobuf.Empty) returns (BlockRef);
```

**Description:** Retrieves the next expected block number from the state database.

**Input:** Empty request.

**Output: `BlockRef`**
| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | The next expected block number |

**Example:**

```go
blockRef, _ := client.GetNextBlockNumberToCommit(ctx, &emptypb.Empty{})
fmt.Printf("Next block: %d\n", blockRef.Number)
```

---

### 4.4 GetTransactionsStatus

```protobuf
rpc GetTransactionsStatus(TxIDsBatch) returns (TxStatusBatch);
```

**Description:** Queries transaction statuses from the database by transaction IDs.

**Input: `TxIDsBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `tx_ids` | `repeated string` | List of transaction IDs to query |

**Output: `TxStatusBatch`**
| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Status for each queried transaction |

**Example:**

```go
resp, _ := client.GetTransactionsStatus(ctx, &committerpb.TxIDsBatch{
    TxIds: []string{"tx-abc-123", "tx-def-456"},
})
for _, s := range resp.Status {
    fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
}
```

---

### 4.5 GetNamespacePolicies

```protobuf
rpc GetNamespacePolicies(google.protobuf.Empty) returns (NamespacePolicies);
```

**Description:** Retrieves all namespace endorsement policies from the database.

**Input:** Empty request.

**Output: `NamespacePolicies`**
| Field | Type | Description |
|-------|------|-------------|
| `policies` | `repeated PolicyItem` | List of namespace policies |

**Expanded: `PolicyItem`**
| Field | Type | Description |
|-------|------|-------------|
| `namespace` | `string` | Namespace identifier |
| `policy` | `bytes` | Serialized endorsement policy |
| `version` | `uint64` | Policy version number |

**Example:**

```go
policies, _ := client.GetNamespacePolicies(ctx, &emptypb.Empty{})
for _, p := range policies.Policies {
    fmt.Printf("Namespace %s: version %d\n", p.Namespace, p.Version)
}
```

---

### 4.6 GetConfigTransaction

```protobuf
rpc GetConfigTransaction(google.protobuf.Empty) returns (ConfigTransaction);
```

**Description:** Retrieves the latest system configuration transaction.

**Input:** Empty request.

**Output: `ConfigTransaction`**
| Field | Type | Description |
|-------|------|-------------|
| `envelope` | `bytes` | Serialized configuration envelope |
| `version` | `uint64` | Configuration version number |

**Example:**

```go
config, _ := client.GetConfigTransaction(ctx, &emptypb.Empty{})
fmt.Printf("Config version: %d\n", config.Version)
```

---

### 4.7 SetupSystemTablesAndNamespaces

```protobuf
rpc SetupSystemTablesAndNamespaces(google.protobuf.Empty) returns (google.protobuf.Empty);
```

**Description:** Initializes the system database tables and namespaces. Called during initial setup.

**Input:** Empty request.

**Output:** Empty response on success.

**Example:**

```go
_, err := client.SetupSystemTablesAndNamespaces(ctx, &emptypb.Empty{})
```

---

## 5. Query Service

**Proto file:** `api/committerpb/query.proto` (from fabric-x-common)
**Package:** `committerpb`
**Service:** `QueryService`

The Query service provides read-only access to the state database with view-based isolation guarantees. It supports different isolation levels for querying committed state.

### 5.1 BeginView

```protobuf
rpc BeginView(ViewParameters) returns (View);
```

**Description:** Creates a new database view with the specified isolation level. The view provides a consistent snapshot for subsequent queries.

**Input: `ViewParameters`**
| Field | Type | Description |
|-------|------|-------------|
| `iso_level` | `IsoLevel` | Isolation level for the view |
| `non_deferrable` | `bool` | If true, do not defer errors (default: deferrable) |
| `timeout_milliseconds` | `uint64` | View timeout in milliseconds (0 = maximum) |

**IsoLevel Enum:**
| Value | Description |
|-------|-------------|
| `ISO_LEVEL_UNSPECIFIED` | Defaults to SERIALIZABLE |
| `SERIALIZABLE` | Full serializable isolation |
| `REPEATABLE_READ` | Repeatable read isolation |
| `READ_COMMITTED` | Read committed isolation |
| `READ_UNCOMMITTED` | Read uncommitted isolation |

**Output: `View`**
| Field | Type | Description |
|-------|------|-------------|
| `id` | `string` | View identifier for subsequent operations |

**Example:**

```go
view, _ := client.BeginView(ctx, &committerpb.ViewParameters{
    IsoLevel:            committerpb.SERIALIZABLE,
    TimeoutMilliseconds: 30000,
})
defer client.EndView(ctx, view)
// Use view.Id for subsequent queries
```

---

### 5.2 EndView

```protobuf
rpc EndView(View) returns (google.protobuf.Empty);
```

**Description:** Closes an open view and releases associated database resources.

**Input: `View`**
| Field | Type | Description |
|-------|------|-------------|
| `id` | `string` | View identifier to close |

**Output:** Empty response on success.

**Example:**

```go
_, err := client.EndView(ctx, &committerpb.View{Id: viewID})
```

---

### 5.3 GetRows

```protobuf
rpc GetRows(Query) returns (Rows);
```

**Description:** Retrieves key-value pairs from specified namespaces. Can operate within an existing view or create an implicit single-query view.

**Input: `Query`**
| Field | Type | Description |
|-------|------|-------------|
| `view` | `optional View` | Existing view to use (optional) |
| `namespaces` | `repeated QueryNamespace` | Namespaces and keys to query |

**Expanded: `QueryNamespace`**
| Field | Type | Description |
|-------|------|-------------|
| `ns_id` | `string` | Namespace identifier |
| `keys` | `repeated bytes` | Keys to retrieve |

**Output: `Rows`**
| Field | Type | Description |
|-------|------|-------------|
| `namespaces` | `repeated RowsNamespace` | Results grouped by namespace |

**Expanded: `RowsNamespace`**
| Field | Type | Description |
|-------|------|-------------|
| `ns_id` | `string` | Namespace identifier |
| `rows` | `repeated Row` | Retrieved rows |

**Expanded: `Row`**
| Field | Type | Description |
|-------|------|-------------|
| `key` | `bytes` | The key |
| `value` | `bytes` | The value |
| `version` | `uint64` | Value version |

**Example:**

```go
rows, _ := client.GetRows(ctx, &committerpb.Query{
    View: &committerpb.View{Id: viewID}, // optional
    Namespaces: []*committerpb.QueryNamespace{
        {NsId: "myns", Keys: [][]byte{[]byte("key1"), []byte("key2")}},
    },
})
for _, ns := range rows.Namespaces {
    for _, row := range ns.Rows {
        fmt.Printf("%s = %s (v%d)\n", row.Key, row.Value, row.Version)
    }
}
```

---

### 5.4 GetNamespacePolicies

```protobuf
rpc GetNamespacePolicies(google.protobuf.Empty) returns (NamespacePolicies);
```

**Description:** Retrieves all namespace endorsement policies.

**Input:** Empty request.

**Output: `NamespacePolicies`**
| Field | Type | Description |
|-------|------|-------------|
| `policies` | `repeated PolicyItem` | List of namespace policies |

**Example:**

```go
policies, _ := client.GetNamespacePolicies(ctx, &emptypb.Empty{})
for _, p := range policies.Policies {
    fmt.Printf("Namespace %s: version %d\n", p.Namespace, p.Version)
}
```

---

### 5.5 GetConfigTransaction

```protobuf
rpc GetConfigTransaction(google.protobuf.Empty) returns (ConfigTransaction);
```

**Description:** Retrieves the latest system configuration transaction.

**Input:** Empty request.

**Output: `ConfigTransaction`**
| Field | Type | Description |
|-------|------|-------------|
| `envelope` | `bytes` | Serialized configuration envelope |
| `version` | `uint64` | Configuration version number |

**Example:**

```go
config, _ := client.GetConfigTransaction(ctx, &emptypb.Empty{})
fmt.Printf("Config version: %d\n", config.Version)
```

---

### 5.6 GetTransactionStatus

```protobuf
rpc GetTransactionStatus(TxStatusQuery) returns (TxStatusResponse);
```

**Description:** Queries the status of transactions by their IDs within an optional view.

**Input: `TxStatusQuery`**
| Field | Type | Description |
|-------|------|-------------|
| `view` | `optional View` | Existing view to use (optional) |
| `tx_ids` | `repeated string` | Transaction IDs to query |

**Output: `TxStatusResponse`**
| Field | Type | Description |
|-------|------|-------------|
| `statuses` | `repeated TxStatus` | Status for each queried transaction |

**Example:**

```go
resp, _ := client.GetTransactionStatus(ctx, &committerpb.TxStatusQuery{
    TxIds: []string{"tx-abc-123", "tx-def-456"},
})
for _, s := range resp.Statuses {
    fmt.Printf("Tx %s: %v\n", s.Ref.TxId, s.Status)
}
```

---

## 6. Common Message Types

This section documents shared message types used across multiple services.

### 6.1 BlockRef

**Package:** `servicepb`

| Field | Type | Description |
|-------|------|-------------|
| `number` | `uint64` | Block number from the orderer |

### 6.2 TxRef

**Package:** `committerpb`

| Field | Type | Description |
|-------|------|-------------|
| `block_num` | `uint64` | Block number |
| `tx_num` | `uint32` | Transaction index within the block |
| `tx_id` | `string` | Transaction ID |

### 6.3 TxStatus

**Package:** `committerpb`

| Field | Type | Description |
|-------|------|-------------|
| `ref` | `TxRef` | Transaction reference |
| `status` | `Status` | Validation result (see Status Codes) |

### 6.4 TxStatusBatch

**Package:** `committerpb`

| Field | Type | Description |
|-------|------|-------------|
| `status` | `repeated TxStatus` | Batch of transaction statuses |

### 6.5 TxIDsBatch

**Package:** `committerpb`

| Field | Type | Description |
|-------|------|-------------|
| `tx_ids` | `repeated string` | Batch of transaction IDs |

### 6.6 ConfigTransaction

**Package:** `applicationpb`

| Field | Type | Description |
|-------|------|-------------|
| `envelope` | `bytes` | Serialized configuration envelope |
| `version` | `uint64` | Configuration version number |

### 6.7 NamespacePolicies

**Package:** `applicationpb`

| Field | Type | Description |
|-------|------|-------------|
| `policies` | `repeated PolicyItem` | List of namespace policies |

### 6.8 PolicyItem

**Package:** `applicationpb`

| Field | Type | Description |
|-------|------|-------------|
| `namespace` | `string` | Namespace identifier |
| `policy` | `bytes` | Serialized endorsement policy |
| `version` | `uint64` | Policy version number |

---

## 7. Status Codes

Transaction validation results are represented by the `Status` enum from `committerpb`.

### Successful Status

| Code | Name | Description |
|------|------|-------------|
| `1` | `COMMITTED` | Transaction successfully committed and state updated |

### Validation Failures (Stored in State DB)

These statuses are recorded and prevent resubmission of the same transaction ID.

| Code | Name | Description |
|------|------|-------------|
| `2` | `ABORTED_SIGNATURE_INVALID` | Signature invalid according to namespace policy |
| `3` | `ABORTED_MVCC_CONFLICT` | Read-write set conflict (stale read) |

### Rejection Statuses (Not Stored)

These statuses indicate the transaction ID could not be extracted or is already occupied.

| Code | Name | Description |
|------|------|-------------|
| `100` | `REJECTED_DUPLICATE_TX_ID` | Transaction with same ID already processed |
| `101` | `MALFORMED_BAD_ENVELOPE` | Cannot unmarshal envelope |
| `102` | `MALFORMED_MISSING_TX_ID` | Envelope missing transaction ID |

### Malformed Transaction Statuses (Stored in State DB)

| Code | Name | Description |
|------|------|-------------|
| `103` | `MALFORMED_UNSUPPORTED_ENVELOPE_PAYLOAD` | Unsupported envelope payload type |
| `104` | `MALFORMED_BAD_ENVELOPE_PAYLOAD` | Cannot unmarshal envelope payload |
| `105` | `MALFORMED_TX_ID_CONFLICT` | Envelope TX ID doesn't match payload TX ID |
| `106` | `MALFORMED_EMPTY_NAMESPACES` | No namespaces provided |
| `107` | `MALFORMED_DUPLICATE_NAMESPACE` | Duplicate namespace detected |
| `108` | `MALFORMED_NAMESPACE_ID_INVALID` | Invalid namespace identifier |
| `109` | `MALFORMED_BLIND_WRITES_NOT_ALLOWED` | Blind writes not allowed in namespace |
| `110` | `MALFORMED_NO_WRITES` | No write operations in transaction |
| `111` | `MALFORMED_EMPTY_KEY` | Unset key detected |
| `112` | `MALFORMED_DUPLICATE_KEY_IN_READ_WRITE_SET` | Duplicate key in read-write set |
| `113` | `MALFORMED_MISSING_SIGNATURE` | Signature count doesn't match namespace count |
| `114` | `MALFORMED_NAMESPACE_POLICY_INVALID` | Invalid namespace policy |
| `115` | `MALFORMED_CONFIG_TX_INVALID` | Invalid configuration transaction |

### Internal Status

| Code | Name | Description |
|------|------|-------------|
| `0` | `STATUS_UNSPECIFIED` | Default; transaction not yet validated (never persisted) |