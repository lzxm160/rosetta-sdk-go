// Copyright 2020 Coinbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Generated by: OpenAPI Generator (https://openapi-generator.tech)

package types

// SyncStatus SyncStatus is used to provide additional context about an implementation's sync
// status. It is often used to indicate that an implementation is healthy when it cannot be queried
// until some sync phase occurs. If an implementation is immediately queryable, this model is often
// not populated.
type SyncStatus struct {
	// CurrentIndex is the index of the last synced block in the current stage.
	CurrentIndex int64 `json:"current_index"`
	// TargetIndex is the index of the block that the implementation is attempting to sync to in the
	// current stage.
	TargetIndex *int64 `json:"target_index,omitempty"`
	// Stage is the phase of the sync process.
	Stage *string `json:"stage,omitempty"`
}
