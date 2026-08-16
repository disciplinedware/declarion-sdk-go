package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Go consumer cannot act on a report it never receives. This surface decoded
// {status, audit_operation_id, result} and dropped the chain on the floor, so a
// caller that waited for the chain read "success" and moved on.
func TestInvokeCarriesTheChainReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{"lead_id":"lead-1"},
			"events":{"failed_count":1,"failures":[
				{"step":2,"handler":"lead.enrich","object":"lead-7","not_attempted":3,
				 "error":{"type":"/errors/job.failed","title":"did not complete","retryable":false}}]}}`))
	}))
	t.Cleanup(srv.Close)

	res, err := New(Config{BaseURL: srv.URL}).Actions().Invoke(context.Background(), "lead.upsert", InvokeParams{})
	require.NoError(t, err)
	assert.Equal(t, "success", res.Status, "a failed event does not fail the parent: it committed")

	require.True(t, res.Events.Failed(), "the caller must be able to tell 'saved' from 'saved, and the work did not run'")
	require.Len(t, res.Events.Failures, 1)
	f := res.Events.Failures[0]
	assert.Equal(t, 2, f.Step)
	assert.Equal(t, "lead.enrich", f.Handler)
	assert.Equal(t, "lead-7", f.Object)
	assert.Equal(t, 3, f.NotAttempted)
	assert.Equal(t, "job.failed", f.Error.Code())
}

// Absence means "there was no such step", never "the step passed".
func TestNoChainMeansNoReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{}}`))
	}))
	t.Cleanup(srv.Close)

	res, err := New(Config{BaseURL: srv.URL}).Actions().Invoke(context.Background(), "lead.upsert", InvokeParams{})
	require.NoError(t, err)
	assert.Nil(t, res.Events)
	assert.False(t, res.Events.Failed(), "nil is not a failure, and asking must not panic")
}
