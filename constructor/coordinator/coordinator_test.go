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

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/coinbase/rosetta-sdk-go/asserter"
	"github.com/coinbase/rosetta-sdk-go/constructor/executor"
	mocks "github.com/coinbase/rosetta-sdk-go/mocks/constructor/coordinator"
	"github.com/coinbase/rosetta-sdk-go/parser"
	"github.com/coinbase/rosetta-sdk-go/storage"
	"github.com/coinbase/rosetta-sdk-go/types"
	"github.com/coinbase/rosetta-sdk-go/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func simpleAsserterConfiguration() (*asserter.Asserter, error) {
	return asserter.NewClientWithOptions(
		&types.NetworkIdentifier{
			Blockchain: "bitcoin",
			Network:    "mainnet",
		},
		&types.BlockIdentifier{
			Hash:  "block 0",
			Index: 0,
		},
		[]string{"Transfer"},
		[]*types.OperationStatus{
			{
				Status:     "success",
				Successful: true,
			},
			{
				Status:     "failure",
				Successful: false,
			},
		},
		[]*types.Error{},
	)
}

func defaultParser(t *testing.T) *parser.Parser {
	asserter, err := simpleAsserterConfiguration()
	assert.NoError(t, err)

	return parser.New(asserter, nil)
}

func TestProcess(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	jobStorage := &mocks.JobStorage{}
	helper := &mocks.Helper{}
	p := defaultParser(t)
	workflows := []*executor.Workflow{
		{
			Name:        string(executor.RequestFunds),
			Concurrency: 1,
			Scenarios: []*executor.Scenario{
				{
					Name: "request_funds",
					Actions: []*executor.Action{
						{
							Type:       executor.SetVariable,
							Input:      `{"symbol":"tBTC", "decimals":8}`,
							OutputPath: "currency",
						},
						{ // ensure we have some balance that exists
							Type:       executor.FindBalance,
							Input:      `{"minimum_balance":{"value": "0", "currency": {{currency}}}, "create":1}`, // nolint
							OutputPath: "random_address",
						},
						{
							Type:       executor.FindBalance,
							Input:      `{"address": {{random_address.account.address}}, "wait": true, "minimum_balance":{"value": "100", "currency": {{currency}}}}`, // nolint
							OutputPath: "loaded_address",
						},
					},
				},
			},
		},
		{
			Name:        string(executor.CreateAccount),
			Concurrency: 1,
			Scenarios: []*executor.Scenario{
				{
					Name: "create_account",
					Actions: []*executor.Action{
						{
							Type:       executor.SetVariable,
							Input:      `{"network":"Testnet3", "blockchain":"Bitcoin"}`,
							OutputPath: "network",
						},
						{
							Type:       executor.GenerateKey,
							Input:      `{"curve_type": "secp256k1"}`,
							OutputPath: "key",
						},
						{
							Type:       executor.Derive,
							Input:      `{"network_identifier": {{network}}, "public_key": {{key.public_key}}}`,
							OutputPath: "address",
						},
						{
							Type:  executor.SaveAddress,
							Input: `{"address": {{address.address}}, "keypair": {{key.public_key}}}`,
						},
					},
				},
			},
		},
		{
			Name:        "transfer",
			Concurrency: 1,
			Scenarios: []*executor.Scenario{
				{
					Name: "transfer",
					Actions: []*executor.Action{
						{
							Type:       executor.SetVariable,
							Input:      `{"network":"Testnet3", "blockchain":"Bitcoin"}`,
							OutputPath: "transfer.network",
						},
						{
							Type:       executor.SetVariable,
							Input:      `{"symbol":"tBTC", "decimals":8}`,
							OutputPath: "currency",
						},
						{
							Type:       executor.FindBalance,
							Input:      `{"minimum_balance":{"value": "100", "currency": {{currency}}}, "create": 100}`, // nolint
							OutputPath: "sender",
						},
						{
							Type:       executor.Math,
							Input:      `{"operation":"subtraction", "left_value": "0", "right_value":{{sender.balance.value}}}`,
							OutputPath: "sender_amount",
						},
						{
							Type:       executor.FindBalance,
							Input:      `{"not_address":[{{sender.account.address}}], "minimum_balance":{"value": "0", "currency": {{currency}}}, "create": 100}`, // nolint
							OutputPath: "recipient",
						},
						{
							Type:       executor.Math,
							Input:      `{"operation":"subtraction", "left_value":{{sender.balance.value}}, "right_value":"10"}`,
							OutputPath: "recipient_amount",
						},
						{
							Type:       executor.SetVariable,
							Input:      `"1"`,
							OutputPath: "transfer.confirmation_depth",
						},
						{
							Type:       executor.SetVariable,
							Input:      `{"test": "works"}`,
							OutputPath: "transfer.preprocess_metadata",
						},
						{
							Type:       executor.SetVariable,
							Input:      `[{"operation_identifier":{"index":0},"type":"Vin","status":"","account":{"address":{{sender.account.address}}},"amount":{"value":{{sender_amount}},"currency":{{currency}}}},{"operation_identifier":{"index":1},"type":"Vout","status":"","account":{"address":{{recipient.account.address}}},"amount":{"value":{{recipient_amount}},"currency":{{currency}}}}]`, // nolint
							OutputPath: "transfer.operations",
						},
					},
				},
				{
					Name: "print_transaction",
					Actions: []*executor.Action{
						{
							Type:  executor.PrintMessage,
							Input: `{{transfer.transaction}}`,
						},
					},
				},
			},
		},
	}

	c, err := NewCoordinator(
		jobStorage,
		helper,
		p,
		workflows,
	)
	assert.NotNil(t, c)
	assert.NoError(t, err)

	// Create coordination channels
	processCanceled := make(chan struct{})

	dir, err := utils.CreateTempDir()
	assert.NoError(t, err)

	db, err := storage.NewBadgerStorage(ctx, dir)
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// HeadBlockExists is false first
	helper.On("HeadBlockExists", ctx).Return(false).Once()
	helper.On("HeadBlockExists", ctx).Return(true).Once()

	// Attempt to transfer
	// We use a "read" database transaction in this test because we mock
	// all responses from the database and "write" transactions require a
	// lock. While it would be possible to orchestrate these locks in this
	// test, it is simpler to just use a "read" transaction.
	dbTxFail := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTxFail).Once()
	jobStorage.On("Ready", ctx, dbTxFail).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTxFail, "transfer").Return(0, nil).Once()
	helper.On("AllAddresses", ctx, dbTxFail).Return([]string{}, nil).Once()

	// Start processor
	go func() {
		err := c.Process(ctx)
		fmt.Println(err)
		assert.True(t, errors.Is(err, context.Canceled))
		close(processCanceled)
	}()

	// Determine account must be created
	helper.On("HeadBlockExists", ctx).Return(true).Once()

	dbTx := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx).Once()
	jobStorage.On("Ready", ctx, dbTx).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Broadcasting", ctx, dbTx).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTx, "create_account").Return(0, nil).Once()
	helper.On(
		"Derive",
		ctx,
		&types.NetworkIdentifier{
			Blockchain: "Bitcoin",
			Network:    "Testnet3",
		},
		mock.Anything,
		(map[string]interface{})(nil),
	).Return("address1", nil, nil).Once()
	helper.On(
		"StoreKey",
		ctx,
		dbTx,
		"address1",
		mock.Anything,
	).Return(nil).Once()
	jobStorage.On("Update", ctx, dbTx, mock.Anything).Return("job1", nil).Once()
	helper.On("BroadcastAll", ctx).Return(nil).Once()

	// Attempt to run transfer again (but determine funds are needed)
	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTxFail2 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTxFail2).Once()
	jobStorage.On("Ready", ctx, dbTxFail2).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTxFail2, "transfer").Return(0, nil).Once()
	helper.On("AllAddresses", ctx, dbTxFail2).Return([]string{"address1"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTxFail2).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTxFail2,
		&types.AccountIdentifier{Address: "address1"},
	).Return(
		[]*types.Amount{
			{
				Value: "0",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()

	// Attempt funds request
	helper.On("HeadBlockExists", ctx).Return(true).Once()

	dbTx2 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx2).Once()
	jobStorage.On("Ready", ctx, dbTx2).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Broadcasting", ctx, dbTx2).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTx2, "request_funds").Return(0, nil).Once()
	helper.On("AllAddresses", ctx, dbTx2).Return([]string{"address1"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTx2).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTx2,
		&types.AccountIdentifier{Address: "address1"},
	).Return(
		[]*types.Amount{
			{
				Value: "0",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()

	// Load funds
	helper.On("AllAddresses", ctx, dbTx2).Return([]string{"address1"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTx2).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTx2,
		&types.AccountIdentifier{Address: "address1"},
	).Return(
		[]*types.Amount{
			{
				Value: "100",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()

	// Wait until we get here to continue setting up mocks
	jobStorage.On("Update", ctx, dbTx2, mock.Anything).Return("job2", nil).Once()
	helper.On("BroadcastAll", ctx).Return(nil).Once()

	// Attempt to transfer again
	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTxFail3 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTxFail3).Once()
	jobStorage.On("Ready", ctx, dbTxFail3).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTxFail3, "transfer").Return(0, nil).Once()
	helper.On("AllAddresses", ctx, dbTxFail3).Return([]string{"address1"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTxFail3).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTxFail3,
		&types.AccountIdentifier{Address: "address1"},
	).Return(
		[]*types.Amount{
			{
				Value: "100",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()
	helper.On("AllAddresses", ctx, dbTxFail3).Return([]string{"address1"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTxFail3).Return([]string{}, nil).Once()

	// Attempt to create recipient
	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTx3 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx3).Once()
	jobStorage.On("Ready", ctx, dbTx3).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Broadcasting", ctx, dbTx3).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTx3, "create_account").Return(0, nil).Once()
	helper.On(
		"Derive",
		ctx,
		&types.NetworkIdentifier{
			Blockchain: "Bitcoin",
			Network:    "Testnet3",
		},
		mock.Anything,
		(map[string]interface{})(nil),
	).Return("address2", nil, nil).Once()
	helper.On(
		"StoreKey",
		ctx,
		dbTx3,
		"address2",
		mock.Anything,
	).Return(nil).Once()
	jobStorage.On("Update", ctx, dbTx3, mock.Anything).Return("job3", nil).Once()
	helper.On("BroadcastAll", ctx).Return(nil).Once()

	// Attempt to create transfer
	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTx4 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx4).Once()
	jobStorage.On("Ready", ctx, dbTx4).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTx4, "transfer").Return(0, nil).Once()
	helper.On("AllAddresses", ctx, dbTx4).Return([]string{"address1", "address2"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTx4).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTx4,
		&types.AccountIdentifier{Address: "address1"},
	).Return(
		[]*types.Amount{
			{
				Value: "100",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()
	helper.On("AllAddresses", ctx, dbTx4).Return([]string{"address1", "address2"}, nil).Once()
	helper.On("LockedAddresses", ctx, dbTx4).Return([]string{}, nil).Once()
	helper.On(
		"Balance",
		ctx,
		dbTx4,
		&types.AccountIdentifier{Address: "address2"},
	).Return(
		[]*types.Amount{
			{
				Value: "0",
				Currency: &types.Currency{
					Symbol:   "tBTC",
					Decimals: 8,
				},
			},
		},
		nil,
	).Once()
	var job4 *executor.Job
	jobStorage.On(
		"Update",
		ctx,
		dbTx4,
		mock.Anything,
	).Return(
		"job4",
		nil,
	).Run(
		func(args mock.Arguments) {
			job4 = args.Get(2).(*executor.Job)
		},
	).Once()

	// Construct Transaction
	network := &types.NetworkIdentifier{
		Blockchain: "Bitcoin",
		Network:    "Testnet3",
	}
	currency := &types.Currency{
		Symbol:   "tBTC",
		Decimals: 8,
	}
	ops := []*types.Operation{
		{
			OperationIdentifier: &types.OperationIdentifier{
				Index: 0,
			},
			Type: "Vin",
			Account: &types.AccountIdentifier{
				Address: "address1",
			},
			Amount: &types.Amount{
				Value:    "-100",
				Currency: currency,
			},
		},
		{
			OperationIdentifier: &types.OperationIdentifier{
				Index: 1,
			},
			Type: "Vout",
			Account: &types.AccountIdentifier{
				Address: "address2",
			},
			Amount: &types.Amount{
				Value:    "90",
				Currency: currency,
			},
		},
	}
	metadataOptions := map[string]interface{}{
		"metadata": "test",
	}
	helper.On(
		"Preprocess",
		ctx,
		network,
		ops,
		map[string]interface{}{
			"test": "works",
		},
	).Return(metadataOptions, nil).Once()
	fetchedMetadata := map[string]interface{}{
		"tx_meta": "help",
	}
	helper.On(
		"Metadata",
		ctx,
		network,
		metadataOptions,
	).Return(fetchedMetadata, nil).Once()

	unsignedTx := "unsigned transaction"
	signingPayloads := []*types.SigningPayload{
		{
			Address:       "address1",
			Bytes:         []byte("blah"),
			SignatureType: types.Ecdsa,
		},
	}
	helper.On(
		"Payloads",
		ctx,
		network,
		ops,
		fetchedMetadata,
	).Return(unsignedTx, signingPayloads, nil).Once()
	helper.On(
		"Parse",
		ctx,
		network,
		false,
		unsignedTx,
	).Return(ops, []string{}, nil, nil).Once()
	signatures := []*types.Signature{
		{
			SigningPayload: signingPayloads[0],
			PublicKey: &types.PublicKey{
				Bytes:     []byte("pubkey"),
				CurveType: types.Secp256k1,
			},
			SignatureType: types.Ecdsa,
			Bytes:         []byte("signature"),
		},
	}
	helper.On(
		"Sign",
		ctx,
		signingPayloads,
	).Return(signatures, nil).Once()
	networkTx := "network transaction"
	helper.On(
		"Combine",
		ctx,
		network,
		unsignedTx,
		signatures,
	).Return(networkTx, nil).Once()
	helper.On(
		"Parse",
		ctx,
		network,
		true,
		networkTx,
	).Return(ops, []string{"address1"}, nil, nil).Once()
	txIdentifier := &types.TransactionIdentifier{Hash: "transaction hash"}
	helper.On(
		"Hash",
		ctx,
		network,
		networkTx,
	).Return(txIdentifier, nil).Once()
	helper.On(
		"Broadcast",
		ctx,
		dbTx4,
		"job4",
		network,
		ops,
		txIdentifier,
		networkTx,
	).Return(nil).Once()
	helper.On("BroadcastAll", ctx).Return(nil).Once()

	// Wait for transfer to complete
	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTx5 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx5).Once()
	jobStorage.On("Ready", ctx, dbTx5).Return([]*executor.Job{}, nil).Once()
	jobStorage.On("Processing", ctx, dbTx5, "transfer").Return(1, nil).Once()

	markConfirmed := make(chan struct{})
	jobStorage.On("Broadcasting", ctx, dbTx5).Return([]*executor.Job{
		job4,
	}, nil).Run(func(args mock.Arguments) {
		close(markConfirmed)
	}).Once()

	<-markConfirmed
	jobStorage.On("Get", ctx, dbTx5, "job4").Return(job4, nil).Once()
	jobStorage.On(
		"Update",
		ctx,
		dbTx5,
		mock.Anything,
	).Run(func(args mock.Arguments) {
		job4 = args.Get(2).(*executor.Job)
	}).Return(
		"job4",
		nil,
	)
	tx := &types.Transaction{
		TransactionIdentifier: txIdentifier,
		Operations:            ops,
	}

	// Process second step of job4
	dbTx6 := db.NewDatabaseTransaction(ctx, false)
	err = c.BroadcastComplete(ctx, dbTx6, "job4", tx)
	assert.NoError(t, err)

	helper.On("HeadBlockExists", ctx).Return(true).Once()
	dbTx7 := db.NewDatabaseTransaction(ctx, false)
	helper.On("DatabaseTransaction", ctx).Return(dbTx7).Once()
	jobStorage.On("Ready", ctx, dbTx7).Return([]*executor.Job{job4}, nil).Once()
	jobStorage.On(
		"Update",
		ctx,
		dbTx7,
		mock.Anything,
	).Return(
		"job4",
		nil,
	)
	helper.On("BroadcastAll", ctx).Return(nil).Run(func(args mock.Arguments) {
		cancel()
	}).Once()

	<-processCanceled
	jobStorage.AssertExpectations(t)
	helper.AssertExpectations(t)
}
