package statedb

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInstanceSnapshotsPreserveEveryPersistedColumn(t *testing.T) {
	rowType := reflect.TypeOf(InstanceRow{})
	for field := 0; field < rowType.NumField(); field++ {
		name := rowType.Field(field).Name
		if name == "ID" || name == "ToolData" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			require.NoError(t, db.SaveInstance(&InstanceRow{ID: "shared", Title: "original", CreatedAt: time.Unix(100, 0)}))
			rows, err := db.LoadInstances()
			require.NoError(t, err)
			base := rows[0]
			other, desired := CloneInstanceRow(base), CloneInstanceRow(base)
			value := reflect.ValueOf(other).Elem().Field(field)
			switch value.Kind() {
			case reflect.String:
				value.SetString("other writer")
			case reflect.Bool:
				value.SetBool(true)
			case reflect.Int:
				value.SetInt(42)
			case reflect.Struct:
				value.Set(reflect.ValueOf(time.Unix(12345, 0)))
			default:
				t.Fatalf("add a persisted-column fixture for %s", name)
			}
			_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: other}}, nil)
			require.NoError(t, err)
			if name == "Title" {
				desired.Status = "running"
			} else {
				desired.Title = "alice"
			}
			_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
			require.NoError(t, err)
			rows, err = db.LoadInstances()
			require.NoError(t, err)
			got := reflect.ValueOf(rows[0]).Elem().Field(field).Interface()
			if timestamp, ok := value.Interface().(time.Time); ok {
				require.Equal(t, timestamp.Unix(), got.(time.Time).Unix())
			} else {
				require.Equal(t, value.Interface(), got)
			}
		})
	}
}

func TestInstanceSnapshotsToolDataAndAcknowledgement(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.SaveInstance(&InstanceRow{ID: "shared", Title: "original", ToolData: json.RawMessage(`{"notes":"old","custom":{"counter":9007199254740993},"codex_session_id":"kept"}`)}))
	rows, err := db.LoadInstances()
	require.NoError(t, err)
	base := rows[0]
	other, desired := CloneInstanceRow(base), CloneInstanceRow(base)
	other.ToolData = json.RawMessage(`{"notes":"bob","color":"203","custom":{"counter":9007199254740993},"codex_session_id":"kept"}`)
	_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: other}}, nil)
	require.NoError(t, err)
	require.NoError(t, db.SetAcknowledged("shared", true))
	desired.Title = "alice"
	committed, err := db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(other.ToolData), string(committed[0].ToolData))
	var ack int
	require.NoError(t, db.DB().QueryRow("SELECT acknowledged FROM instances WHERE id='shared'").Scan(&ack))
	require.Equal(t, 1, ack, "saving metadata must not reset a targeted acknowledgement")
	desired.ToolData = json.RawMessage(`{"notes":"alice"}`)
	_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
	require.ErrorContains(t, err, "tool_data.notes conflict")
	// Copy ownership includes the raw JSON buffer.
	copy := CloneInstanceRow(base)
	copy.ToolData[0] = '['
	require.Equal(t, byte('{'), base.ToolData[0])
}

func TestInstanceSnapshotsRejectCorruptJSONAndIDCollision(t *testing.T) {
	db := newTestDB(t)
	base := &InstanceRow{ID: "shared", Title: "original", ToolData: json.RawMessage(`{}`)}
	require.NoError(t, db.SaveInstance(base))
	_, err := db.MergeInstanceSnapshots([]InstanceSnapshot{{Desired: CloneInstanceRow(base)}}, nil)
	require.ErrorContains(t, err, "insertion conflict")
	_, err = db.DB().Exec("UPDATE instances SET tool_data = 'corrupt' WHERE id='shared'")
	require.NoError(t, err)
	desired := CloneInstanceRow(base)
	desired.Title = "must not save"
	_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
	require.ErrorContains(t, err, "invalid instance tool_data")
	var title string
	require.NoError(t, db.DB().QueryRow("SELECT title FROM instances WHERE id='shared'").Scan(&title))
	require.Equal(t, "original", title)
}
