package methods

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go/network"
	"github.com/stellar/go/support/log"
	"github.com/stellar/go/xdr"

	"github.com/stellar/soroban-rpc/cmd/soroban-rpc/internal/db"
)

func TestGetTransaction(t *testing.T) {
	var (
		ctx          = context.TODO()
		log          = log.DefaultLogger
		store        = db.NewMockTransactionStore("passphrase")
		ledgerReader = db.NewMockLedgerReader(store)
	)
	log.SetLevel(logrus.DebugLevel)

	_, err := GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{"ab"})
	require.EqualError(t, err, "[-32602] unexpected hash length (2)")
	_, err = GetTransaction(ctx, log, store, ledgerReader,
		GetTransactionRequest{"foo                                                              "})
	require.EqualError(t, err, "[-32602] incorrect hash: encoding/hex: invalid byte: U+006F 'o'")

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tx, err := GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{hash})
	require.NoError(t, err)
	require.Equal(t, GetTransactionResponse{Status: TransactionStatusNotFound}, tx)

	meta := txMeta(1, true)
	require.NoError(t, store.InsertTransactions(meta))

	xdrHash := txHash(1)
	hash = hex.EncodeToString(xdrHash[:])
	tx, err = GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{hash})
	require.NoError(t, err)

	expectedTxResult, err := xdr.MarshalBase64(meta.V1.TxProcessing[0].Result.Result)
	require.NoError(t, err)
	expectedEnvelope, err := xdr.MarshalBase64(txEnvelope(1))
	require.NoError(t, err)
	expectedTxMeta, err := xdr.MarshalBase64(meta.V1.TxProcessing[0].TxApplyProcessing)
	require.NoError(t, err)
	require.Equal(t, GetTransactionResponse{
		Status:                TransactionStatusSuccess,
		LatestLedger:          101,
		LatestLedgerCloseTime: 2625,
		OldestLedger:          101,
		OldestLedgerCloseTime: 2625,
		ApplicationOrder:      1,
		FeeBump:               false,
		EnvelopeXdr:           expectedEnvelope,
		ResultXdr:             expectedTxResult,
		ResultMetaXdr:         expectedTxMeta,
		Ledger:                101,
		LedgerCloseTime:       2625,
		DiagnosticEventsXDR:   []string{},
	}, tx)

	// ingest another (failed) transaction
	meta = txMeta(2, false)
	require.NoError(t, store.InsertTransactions(meta))

	// the first transaction should still be there
	tx, err = GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{hash})
	require.NoError(t, err)
	require.Equal(t, GetTransactionResponse{
		Status:                TransactionStatusSuccess,
		LatestLedger:          102,
		LatestLedgerCloseTime: 2650,
		OldestLedger:          101,
		OldestLedgerCloseTime: 2625,
		ApplicationOrder:      1,
		FeeBump:               false,
		EnvelopeXdr:           expectedEnvelope,
		ResultXdr:             expectedTxResult,
		ResultMetaXdr:         expectedTxMeta,
		Ledger:                101,
		LedgerCloseTime:       2625,
		DiagnosticEventsXDR:   []string{},
	}, tx)

	// the new transaction should also be there
	xdrHash = txHash(2)
	hash = hex.EncodeToString(xdrHash[:])

	expectedTxResult, err = xdr.MarshalBase64(meta.V1.TxProcessing[0].Result.Result)
	require.NoError(t, err)
	expectedEnvelope, err = xdr.MarshalBase64(txEnvelope(2))
	require.NoError(t, err)
	expectedTxMeta, err = xdr.MarshalBase64(meta.V1.TxProcessing[0].TxApplyProcessing)
	require.NoError(t, err)

	tx, err = GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{hash})
	require.NoError(t, err)
	require.Equal(t, GetTransactionResponse{
		Status:                TransactionStatusFailed,
		LatestLedger:          102,
		LatestLedgerCloseTime: 2650,
		OldestLedger:          101,
		OldestLedgerCloseTime: 2625,
		ApplicationOrder:      1,
		FeeBump:               false,
		EnvelopeXdr:           expectedEnvelope,
		ResultXdr:             expectedTxResult,
		ResultMetaXdr:         expectedTxMeta,
		Ledger:                102,
		LedgerCloseTime:       2650,
		DiagnosticEventsXDR:   []string{},
	}, tx)

	// Test Txn with events
	meta = txMetaWithEvents(3, true)
	require.NoError(t, store.InsertTransactions(meta))

	xdrHash = txHash(3)
	hash = hex.EncodeToString(xdrHash[:])

	expectedTxResult, err = xdr.MarshalBase64(meta.V1.TxProcessing[0].Result.Result)
	require.NoError(t, err)
	expectedEnvelope, err = xdr.MarshalBase64(txEnvelope(3))
	require.NoError(t, err)
	expectedTxMeta, err = xdr.MarshalBase64(meta.V1.TxProcessing[0].TxApplyProcessing)
	require.NoError(t, err)

	diagnosticEvents, err := meta.V1.TxProcessing[0].TxApplyProcessing.GetDiagnosticEvents()
	require.NoError(t, err)
	expectedEventsMeta, err := xdr.MarshalBase64(diagnosticEvents[0])
	require.NoError(t, err)

	tx, err = GetTransaction(ctx, log, store, ledgerReader, GetTransactionRequest{hash})
	require.NoError(t, err)
	require.Equal(t, GetTransactionResponse{
		Status:                TransactionStatusSuccess,
		LatestLedger:          103,
		LatestLedgerCloseTime: 2675,
		OldestLedger:          101,
		OldestLedgerCloseTime: 2625,
		ApplicationOrder:      1,
		FeeBump:               false,
		EnvelopeXdr:           expectedEnvelope,
		ResultXdr:             expectedTxResult,
		ResultMetaXdr:         expectedTxMeta,
		Ledger:                103,
		LedgerCloseTime:       2675,
		DiagnosticEventsXDR:   []string{expectedEventsMeta},
	}, tx)
}

func ledgerCloseTime(ledgerSequence uint32) int64 {
	return int64(ledgerSequence)*25 + 100
}

func txHash(acctSeq uint32) xdr.Hash {
	envelope := txEnvelope(acctSeq)
	hash, err := network.HashTransactionInEnvelope(envelope, "passphrase")
	if err != nil {
		panic(err)
	}

	return hash
}

func txEnvelope(acctSeq uint32) xdr.TransactionEnvelope {
	envelope, err := xdr.NewTransactionEnvelope(xdr.EnvelopeTypeEnvelopeTypeTx, xdr.TransactionV1Envelope{
		Tx: xdr.Transaction{
			Fee:           1,
			SeqNum:        xdr.SequenceNumber(acctSeq),
			SourceAccount: xdr.MustMuxedAddress("MA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJVAAAAAAAAAAAAAJLK"),
		},
	})
	if err != nil {
		panic(err)
	}
	return envelope
}

func transactionResult(successful bool) xdr.TransactionResult {
	code := xdr.TransactionResultCodeTxBadSeq
	if successful {
		code = xdr.TransactionResultCodeTxSuccess
	}
	opResults := []xdr.OperationResult{}
	return xdr.TransactionResult{
		FeeCharged: 100,
		Result: xdr.TransactionResultResult{
			Code:    code,
			Results: &opResults,
		},
	}
}

func txMeta(acctSeq uint32, successful bool) xdr.LedgerCloseMeta {
	envelope := txEnvelope(acctSeq)

	txProcessing := []xdr.TransactionResultMeta{
		{
			TxApplyProcessing: xdr.TransactionMeta{
				V:          3,
				Operations: &[]xdr.OperationMeta{},
				V3:         &xdr.TransactionMetaV3{},
			},
			Result: xdr.TransactionResultPair{
				TransactionHash: txHash(acctSeq),
				Result:          transactionResult(successful),
			},
		},
	}

	components := []xdr.TxSetComponent{
		{
			Type: xdr.TxSetComponentTypeTxsetCompTxsMaybeDiscountedFee,
			TxsMaybeDiscountedFee: &xdr.TxSetComponentTxsMaybeDiscountedFee{
				BaseFee: nil,
				Txs: []xdr.TransactionEnvelope{
					envelope,
				},
			},
		},
	}
	return xdr.LedgerCloseMeta{
		V: 1,
		V1: &xdr.LedgerCloseMetaV1{
			LedgerHeader: xdr.LedgerHeaderHistoryEntry{
				Header: xdr.LedgerHeader{
					ScpValue: xdr.StellarValue{
						CloseTime: xdr.TimePoint(ledgerCloseTime(acctSeq + 100)),
					},
					LedgerSeq: xdr.Uint32(acctSeq + 100),
				},
			},
			TxProcessing: txProcessing,
			TxSet: xdr.GeneralizedTransactionSet{
				V: 1,
				V1TxSet: &xdr.TransactionSetV1{
					PreviousLedgerHash: xdr.Hash{1},
					Phases: []xdr.TransactionPhase{
						{
							V:            0,
							V0Components: &components,
						},
					},
				},
			},
		},
	}
}

func txMetaWithEvents(acctSeq uint32, successful bool) xdr.LedgerCloseMeta {
	meta := txMeta(acctSeq, successful)

	contractIDBytes, _ := hex.DecodeString("df06d62447fd25da07c0135eed7557e5a5497ee7d15b7fe345bd47e191d8f577")
	var contractID xdr.Hash
	copy(contractID[:], contractIDBytes)
	counter := xdr.ScSymbol("COUNTER")

	meta.V1.TxProcessing[0].TxApplyProcessing.V3 = &xdr.TransactionMetaV3{
		SorobanMeta: &xdr.SorobanTransactionMeta{
			Events: []xdr.ContractEvent{{
				ContractId: &contractID,
				Type:       xdr.ContractEventTypeContract,
				Body: xdr.ContractEventBody{
					V: 0,
					V0: &xdr.ContractEventV0{
						Topics: []xdr.ScVal{{
							Type: xdr.ScValTypeScvSymbol,
							Sym:  &counter,
						}},
						Data: xdr.ScVal{
							Type: xdr.ScValTypeScvSymbol,
							Sym:  &counter,
						},
					},
				},
			}},
			ReturnValue: xdr.ScVal{
				Type: xdr.ScValTypeScvSymbol,
				Sym:  &counter,
			},
		},
	}

	return meta
}
