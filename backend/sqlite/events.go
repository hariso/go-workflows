package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/cschleiden/go-workflows/internal/core"
	"github.com/cschleiden/go-workflows/internal/history"
)

func getPendingEvents(ctx context.Context, tx *sql.Tx, instance *core.WorkflowInstance) ([]*history.Event, error) {
	now := time.Now()
	events, err := tx.QueryContext(
		ctx,
		"SELECT * FROM `pending_events` WHERE instance_id = ? AND execution_id = ? AND (`visible_at` IS NULL OR `visible_at` <= ?)",
		instance.InstanceID,
		instance.ExecutionID,
		now,
	)
	defer events.Close()

	if err != nil {
		return nil, fmt.Errorf("getting new events: %w", err)
	}

	pendingEvents := make([]*history.Event, 0)

	for events.Next() {
		pendingEvent, err := scanEvent(events)
		if err != nil {
			return nil, fmt.Errorf("reading event: %w", err)
		}

		pendingEvents = append(pendingEvents, pendingEvent)
	}

	return pendingEvents, nil
}

func getHistory(ctx context.Context, tx *sql.Tx, instance *core.WorkflowInstance, lastSequenceID *int64) ([]*history.Event, error) {
	var historyEvents *sql.Rows
	var err error
	if lastSequenceID != nil {
		historyEvents, err = tx.QueryContext(
			ctx, "SELECT * FROM `history` WHERE instance_id = ? AND execution_id = ? AND sequence_id > ?", instance.InstanceID, instance.ExecutionID, *lastSequenceID)
	} else {
		historyEvents, err = tx.QueryContext(
			ctx, "SELECT * FROM `history` WHERE instance_id = ? AND execution_id = ?", instance.InstanceID, instance.ExecutionID)
	}
	defer historyEvents.Close()
	if err != nil {
		return nil, fmt.Errorf("getting history: %w", err)
	}

	events := make([]*history.Event, 0)

	for historyEvents.Next() {
		historyEvent, err := scanEvent(historyEvents)
		if err != nil {
			return nil, fmt.Errorf("reading event: %w", err)
		}

		events = append(events, historyEvent)
	}

	return events, nil
}

type Scanner interface {
	Scan(dest ...interface{}) error
}

func scanEvent(row Scanner) (*history.Event, error) {
	var instanceID, executionID string
	var attributes []byte

	historyEvent := &history.Event{}

	if err := row.Scan(
		&historyEvent.ID,
		&historyEvent.SequenceID,
		&instanceID,
		&executionID,
		&historyEvent.Type,
		&historyEvent.Timestamp,
		&historyEvent.ScheduleEventID,
		&attributes,
		&historyEvent.VisibleAt,
	); err != nil {
		return historyEvent, fmt.Errorf("scanning event: %w", err)
	}

	a, err := history.DeserializeAttributes(historyEvent.Type, attributes)
	if err != nil {
		return historyEvent, fmt.Errorf("deserializing attributes: %w", err)
	}

	historyEvent.Attributes = a

	return historyEvent, nil
}

func insertPendingEvents(ctx context.Context, tx *sql.Tx, instance *core.WorkflowInstance, newEvents []*history.Event) error {
	return insertEvents(ctx, tx, "pending_events", instance, newEvents)
}

func insertHistoryEvents(ctx context.Context, tx *sql.Tx, instance *core.WorkflowInstance, historyEvents []*history.Event) error {
	return insertEvents(ctx, tx, "history", instance, historyEvents)
}

func insertEvents(ctx context.Context, tx *sql.Tx, tableName string, instance *core.WorkflowInstance, events []*history.Event) error {
	const batchSize = 20
	for batchStart := 0; batchStart < len(events); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(events) {
			batchEnd = len(events)
		}
		batchEvents := events[batchStart:batchEnd]

		query := "INSERT INTO `" + tableName + "` (id, sequence_id, instance_id, execution_id, event_type, timestamp, schedule_event_id, attributes, visible_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)" +
			strings.Repeat(", (?, ?, ?, ?, ?, ?, ?, ?, ?)", len(batchEvents)-1)

		args := make([]interface{}, 0, len(batchEvents)*7)

		for _, newEvent := range batchEvents {
			a, err := history.SerializeAttributes(newEvent.Attributes)
			if err != nil {
				return err
			}

			args = append(
				args, newEvent.ID, newEvent.SequenceID, instance.InstanceID, instance.ExecutionID, newEvent.Type, newEvent.Timestamp, newEvent.ScheduleEventID, a, newEvent.VisibleAt)
		}

		_, err := tx.ExecContext(
			ctx,
			query,
			args...,
		)

		if err != nil {
			return err
		}
	}
	return nil
}

func removeFutureEvent(ctx context.Context, tx *sql.Tx, instance *core.WorkflowInstance, scheduleEventID int64) error {
	_, err := tx.ExecContext(
		ctx,
		"DELETE FROM `pending_events` WHERE instance_id = ? AND execution_id = ? AND schedule_event_id = ? AND visible_at IS NOT NULL",
		instance.InstanceID,
		instance.ExecutionID,
		scheduleEventID,
	)

	return err
}
