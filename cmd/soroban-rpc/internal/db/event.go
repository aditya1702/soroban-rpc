package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stellar/go/ingest"
	"github.com/stellar/go/support/db"
	"github.com/stellar/go/support/log"
	"github.com/stellar/go/xdr"
)

const (
	eventTableName = "events"
	firstLedger    = uint32(2)
)

// EventWriter is used during ingestion of events from LCM to DB
type EventWriter interface {
	InsertEvents(lcm xdr.LedgerCloseMeta) error
}

// EventReader has all the public methods to fetch events from DB
type EventReader interface {
	GetEvents(ctx context.Context, cursorRange CursorRange, contractIDs [][]byte, f ScanFunction) error
}

type eventHandler struct {
	log                       *log.Entry
	db                        db.SessionInterface
	stmtCache                 *sq.StmtCache
	passphrase                string
	ingestMetric, countMetric prometheus.Observer
}

func NewEventReader(log *log.Entry, db db.SessionInterface, passphrase string) EventReader {
	return &eventHandler{log: log, db: db, passphrase: passphrase}
}

func (eventHandler *eventHandler) InsertEvents(lcm xdr.LedgerCloseMeta) error {
	txCount := lcm.CountTransactions()

	if eventHandler.stmtCache == nil {
		return errors.New("EventWriter incorrectly initialized without stmtCache")
	} else if txCount == 0 {
		return nil
	}

	var txReader *ingest.LedgerTransactionReader
	txReader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(eventHandler.passphrase, lcm)
	if err != nil {
		return fmt.Errorf(
			"failed to open transaction reader for ledger %d: %w ",
			lcm.LedgerSequence(), err)
	}
	defer func() {
		closeErr := txReader.Close()
		if err == nil {
			err = closeErr
		}
	}()

	for {
		var tx ingest.LedgerTransaction
		tx, err = txReader.Read()
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return err
		}

		if !tx.Result.Successful() {
			continue
		}

		txEvents, err := tx.GetDiagnosticEvents()
		if err != nil {
			return err
		}

		if len(txEvents) == 0 {
			continue
		}

		query := sq.Insert(eventTableName).
			Columns("ledger_sequence", "application_order", "contract_id", "event_type")

		for _, e := range txEvents {
			var contractID []byte
			if e.Event.ContractId != nil {
				contractID = e.Event.ContractId[:]
			}
			query = query.Values(lcm.LedgerSequence(), tx.Index, contractID, int(e.Event.Type))
		}

		_, err = query.RunWith(eventHandler.stmtCache).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

type ScanFunction func(
	event xdr.DiagnosticEvent,
	cursor Cursor,
	ledgerCloseTimestamp int64,
	txHash *xdr.Hash,
) bool

// trimEvents removes all Events which fall outside the ledger retention window.
func (eventHandler *eventHandler) trimEvents(latestLedgerSeq uint32, retentionWindow uint32) error {
	if latestLedgerSeq+1 <= retentionWindow {
		return nil
	}

	cutoff := latestLedgerSeq + 1 - retentionWindow
	_, err := sq.StatementBuilder.
		RunWith(eventHandler.stmtCache).
		Delete(eventTableName).
		Where(sq.Lt{"ledger_sequence": cutoff}).
		Exec()
	return err
}

// GetEvents applies f on all the events occurring in the given range with specified contract IDs if provided.
// The events are returned in sorted ascending Cursor order.
// If f returns false, the scan terminates early (f will not be applied on
// remaining events in the range).
func (eventHandler *eventHandler) GetEvents(
	ctx context.Context,
	cursorRange CursorRange,
	contractIDs [][]byte,
	f ScanFunction,
) error {
	start := time.Now()

	var rows []struct {
		TxIndex int                 `db:"application_order"`
		Lcm     xdr.LedgerCloseMeta `db:"meta"`
	}

	rowQ := sq.
		Select("e.application_order", "lcm.meta").
		From(eventTableName + " e").
		Join(ledgerCloseMetaTableName + " lcm ON (e.ledger_sequence = lcm.sequence)").
		Where(sq.GtOrEq{"e.ledger_sequence": cursorRange.Start.Ledger}).
		Where(sq.GtOrEq{"e.application_order": cursorRange.Start.Tx}).
		Where(sq.Lt{"e.ledger_sequence": cursorRange.End.Ledger}).
		OrderBy("e.ledger_sequence ASC")

	if len(contractIDs) > 0 {
		rowQ = rowQ.Where(sq.Eq{"e.contract_id": contractIDs})
	}

	if err := eventHandler.db.Select(ctx, &rows, rowQ); err != nil {
		return fmt.Errorf(
			"db read failed for start ledger cursor= %v contractIDs= %v: %w",
			cursorRange.Start.String(),
			contractIDs,
			err)
	} else if len(rows) < 1 {
		eventHandler.log.Debugf(
			"No events found for ledger range: start ledger cursor= %v - end ledger cursor= %v contractIDs= %v",
			cursorRange.Start.String(),
			cursorRange.End.String(),
			contractIDs,
		)
		return nil
	}

	for _, row := range rows {
		txIndex, lcm := row.TxIndex, row.Lcm
		reader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(eventHandler.passphrase, lcm)
		if err != nil {
			return fmt.Errorf("failed to create ledger reader from LCM: %w", err)
		}

		err = reader.Seek(txIndex - 1)
		if err != nil {
			return fmt.Errorf("failed to index to tx %d in ledger %d: %w", txIndex, lcm.LedgerSequence(), err)
		}

		ledgerCloseTime := lcm.LedgerCloseTime()
		ledgerTx, err := reader.Read()
		if err != nil {
			return fmt.Errorf("failed reading tx: %w", err)
		}
		transactionHash := ledgerTx.Result.TransactionHash
		diagEvents, diagErr := ledgerTx.GetDiagnosticEvents()

		if diagErr != nil {
			return fmt.Errorf("couldn't encode transaction DiagnosticEvents: %w", err)
		}

		// Find events based on filter passed in function f
		for eventIndex, event := range diagEvents {
			cur := Cursor{Ledger: lcm.LedgerSequence(), Tx: uint32(txIndex), Event: uint32(eventIndex)}
			if !f(event, cur, ledgerCloseTime, &transactionHash) {
				return nil
			}
		}
	}

	eventHandler.log.
		WithField("startLedgerSequence", cursorRange.Start.Ledger).
		WithField("endLedgerSequence", cursorRange.End.Ledger).
		WithField("duration", time.Since(start)).
		Debugf("Fetched and decoded all the events with filters - contractIDs: %v ", contractIDs)

	return nil
}

type eventTableMigration struct {
	firstLedger uint32
	lastLedger  uint32
	writer      EventWriter
}

func (e *eventTableMigration) ApplicableRange() *LedgerSeqRange {
	return &LedgerSeqRange{
		FirstLedgerSeq: e.firstLedger,
		LastLedgerSeq:  e.lastLedger,
	}
}

func (e *eventTableMigration) Apply(_ context.Context, meta xdr.LedgerCloseMeta) error {
	return e.writer.InsertEvents(meta)
}

func newEventTableMigration(
	logger *log.Entry,
	retentionWindow uint32,
	passphrase string,
) migrationApplierFactory {
	return migrationApplierFactoryF(func(db *DB, latestLedger uint32) (MigrationApplier, error) {
		firstLedgerToMigrate := firstLedger
		writer := &eventHandler{
			log:        logger,
			db:         db,
			stmtCache:  sq.NewStmtCache(db.GetTx()),
			passphrase: passphrase,
		}
		if latestLedger > retentionWindow {
			firstLedgerToMigrate = latestLedger - retentionWindow
		}

		migration := eventTableMigration{
			firstLedger: firstLedgerToMigrate,
			lastLedger:  latestLedger,
			writer:      writer,
		}
		return &migration, nil
	})
}
