# API gRPC Reference — Checked-in Proto

> **Runtime reconciliation (R3E):** [R3E runtime contract](R3E_RUNTIME_CONTRACT.md) is the authoritative companion for implemented endpoints, supported versus design-only settings, DTOs, and transport-specific limits at this baseline. It supersedes conflicting historical statements. No runtime change is implied.

Each RPC below was matched to a method in internal/grpcapi/threadpool.go or processpool.go. Full field numbers/types are authoritative in api/ccgsc_pools_v1.proto, compiled against generated Go stubs.

| Service | Method | Request | Response | Source |
|---|---|---|---|---|
| `ThreadPool` | `ExchangeToken` | `TokenRequest` | `TokenResponse` | `api/ccgsc_pools_v1.proto:40` |
| `ThreadPool` | `RefreshToken` | `RefreshTokenRequest` | `TokenResponse` | `api/ccgsc_pools_v1.proto:41` |
| `ThreadPool` | `CreateKey` | `CreateKeyRequest` | `ApiKey` | `api/ccgsc_pools_v1.proto:42` |
| `ThreadPool` | `UploadArtifact` | `ArtifactUploadRequest` | `Artifact` | `api/ccgsc_pools_v1.proto:47` |
| `ThreadPool` | `GetArtifact` | `GetArtifactRequest` | `ArtifactData` | `api/ccgsc_pools_v1.proto:50` |
| `ThreadPool` | `DeleteArtifact` | `DeleteArtifactRequest` | `DeleteArtifactResponse` | `api/ccgsc_pools_v1.proto:53` |
| `ThreadPool` | `CreateJob` | `CreateJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:56` |
| `ThreadPool` | `GetJob` | `GetJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:59` |
| `ThreadPool` | `GetJobChain` | `GetJobChainRequest` | `ChainReport` | `api/ccgsc_pools_v1.proto:60` |
| `ThreadPool` | `GetMerkleCheckpoints` | `GetMerkleCheckpointsRequest` | `MerkleReport` | `api/ccgsc_pools_v1.proto:61` |
| `ThreadPool` | `CancelJob` | `CancelJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:64` |
| `ThreadPool` | `GetResults` | `GetResultsRequest` | `ResultsPage` | `api/ccgsc_pools_v1.proto:67` |
| `ThreadPool` | `GetResultsBulk` | `GetResultsBulkRequest` | `ResultsPage` | `api/ccgsc_pools_v1.proto:70` |
| `ThreadPool` | `ValidateJob` | `ValidateJobRequest` | `ValidationResult` | `api/ccgsc_pools_v1.proto:73` |
| `ThreadPool` | `GetCapabilities` | `GetCapabilitiesRequest` | `Capabilities` | `api/ccgsc_pools_v1.proto:76` |
| `ThreadPool` | `StreamEvents` | `EventStreamRequest` | `stream EventMessage` | `api/ccgsc_pools_v1.proto:79` |
| `ProcessPool` | `ExchangeToken` | `TokenRequest` | `TokenResponse` | `api/ccgsc_pools_v1.proto:87` |
| `ProcessPool` | `RefreshToken` | `RefreshTokenRequest` | `TokenResponse` | `api/ccgsc_pools_v1.proto:88` |
| `ProcessPool` | `CreateKey` | `CreateKeyRequest` | `ApiKey` | `api/ccgsc_pools_v1.proto:89` |
| `ProcessPool` | `UploadArtifact` | `ArtifactUploadRequest` | `Artifact` | `api/ccgsc_pools_v1.proto:94` |
| `ProcessPool` | `GetArtifact` | `GetArtifactRequest` | `ArtifactData` | `api/ccgsc_pools_v1.proto:97` |
| `ProcessPool` | `DeleteArtifact` | `DeleteArtifactRequest` | `DeleteArtifactResponse` | `api/ccgsc_pools_v1.proto:100` |
| `ProcessPool` | `CreateJob` | `CreateJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:103` |
| `ProcessPool` | `GetJob` | `GetJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:106` |
| `ProcessPool` | `GetJobChain` | `GetJobChainRequest` | `ChainReport` | `api/ccgsc_pools_v1.proto:107` |
| `ProcessPool` | `GetMerkleCheckpoints` | `GetMerkleCheckpointsRequest` | `MerkleReport` | `api/ccgsc_pools_v1.proto:108` |
| `ProcessPool` | `CancelJob` | `CancelJobRequest` | `Job` | `api/ccgsc_pools_v1.proto:111` |
| `ProcessPool` | `GetResults` | `GetResultsRequest` | `ResultsPage` | `api/ccgsc_pools_v1.proto:114` |
| `ProcessPool` | `GetResultsBulk` | `GetResultsBulkRequest` | `ResultsPage` | `api/ccgsc_pools_v1.proto:117` |
| `ProcessPool` | `ValidateJob` | `ValidateJobRequest` | `ValidationResult` | `api/ccgsc_pools_v1.proto:120` |
| `ProcessPool` | `OpenSession` | `OpenSessionRequest` | `Session` | `api/ccgsc_pools_v1.proto:123` |
| `ProcessPool` | `GetSession` | `GetSessionRequest` | `Session` | `api/ccgsc_pools_v1.proto:126` |
| `ProcessPool` | `StreamSessionTasks` | `StreamSessionTasksRequest` | `SessionTaskBatch` | `api/ccgsc_pools_v1.proto:129` |
| `ProcessPool` | `CloseSession` | `CloseSessionRequest` | `Session` | `api/ccgsc_pools_v1.proto:132` |
| `ProcessPool` | `GetCapabilities` | `GetCapabilitiesRequest` | `Capabilities` | `api/ccgsc_pools_v1.proto:135` |
| `ProcessPool` | `StreamEvents` | `EventStreamRequest` | `stream EventMessage` | `api/ccgsc_pools_v1.proto:138` |

The ProcessPool service additionally supports sessions. Both ThreadPool and ProcessPool expose GetJobChain, which is the existing result-only proof. gRPC bearer authorization, machine codes, retryability, and sanitized error details are defined by internal/grpcapi/errors.go. No new proto fields or RPCs are introduced by F6.

Proto JSON uses lowerCamelCase. Never copy secret values or use draft-only fields as evidence of a shipped feature.
